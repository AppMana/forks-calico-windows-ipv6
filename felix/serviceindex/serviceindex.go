// Copyright (c) 2021 Tigera, Inc. All rights reserved.

// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package serviceindex

import (
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	discovery "k8s.io/api/discovery/v1"

	"github.com/projectcalico/calico/felix/dispatcher"
	"github.com/projectcalico/calico/felix/ip"
	"github.com/projectcalico/calico/felix/labelindex/ipsetmember"
	"github.com/projectcalico/calico/libcalico-go/lib/backend/api"
	"github.com/projectcalico/calico/libcalico-go/lib/backend/model"
)

type ServiceMatchCallback func(ipSetID string, member ipsetmember.IPSetMember)

type ServiceIndex struct {
	services map[string]*v1.Service // Populated only for pre-DNAT ClusterIP matching.
	// cache of all endpoint slices, indexed by service name and slice namespace/name.
	endpointSlices          map[string]*discovery.EndpointSlice
	endpointSlicesByService map[string]map[string]*discovery.EndpointSlice

	// Track active services, indexed by corresponding IP set UID and contributing service.
	activeIPSetsByID      map[string]*ipSetData
	activeIPSetsByService map[string]map[string]*ipSetData

	// Callback functions
	OnMemberAdded   ServiceMatchCallback
	OnMemberRemoved ServiceMatchCallback

	OnAlive        func()
	lastLiveReport time.Time
}

func NewServiceIndex() *ServiceIndex {
	return NewServiceIndexForDataplane(runtime.GOOS == "windows")
}

// NewServiceIndexForDataplane enables frontend tuples for pre-DNAT policy evaluation.
func NewServiceIndexForDataplane(includeClusterIPs bool) *ServiceIndex {
	idx := ServiceIndex{
		endpointSlices:          make(map[string]*discovery.EndpointSlice),
		endpointSlicesByService: make(map[string]map[string]*discovery.EndpointSlice),
		activeIPSetsByID:        make(map[string]*ipSetData),
		activeIPSetsByService:   make(map[string]map[string]*ipSetData),

		// Callback functions
		OnMemberAdded:   func(ipSetID string, member ipsetmember.IPSetMember) {},
		OnMemberRemoved: func(ipSetID string, member ipsetmember.IPSetMember) {},

		OnAlive: func() {},
	}
	if includeClusterIPs {
		idx.services = make(map[string]*v1.Service)
	}
	return &idx
}

func (idx *ServiceIndex) RegisterWith(allUpdDispatcher *dispatcher.Dispatcher) {
	allUpdDispatcher.Register(model.ResourceKey{}, idx.OnUpdate)
}

// OnUpdate makes ServiceIndex compatible with the Dispatcher.  It accepts
// updates for endpoint slices and passes them through to the Update/Delete methods.
func (idx *ServiceIndex) OnUpdate(update api.Update) (_ bool) {
	switch key := update.Key.(type) {
	case model.ResourceKey:
		switch key.Kind {
		case model.KindKubernetesService:
			if idx.services != nil {
				svc, _ := update.Value.(*v1.Service)
				idx.updateService(key.Namespace+"/"+key.Name, svc)
			}
		case model.KindKubernetesEndpointSlice:
			if update.Value != nil {
				logrus.Debugf("Updating ServiceIndex with EndpointSlice %v", key)
				idx.UpdateEndpointSlice(update.Value.(*discovery.EndpointSlice))
			} else {
				logrus.Debugf("Deleting EndpointSlice %v from ServiceIndex", key)
				idx.DeleteEndpointSlice(key)
			}
		}
	}
	return
}

func (idx *ServiceIndex) UpdateEndpointSlice(es *discovery.EndpointSlice) {
	svc := serviceName(es)
	if old := idx.endpointSlices[es.Namespace+"/"+es.Name]; old != nil && serviceName(old) != svc {
		idx.DeleteEndpointSlice(model.ResourceKey{Namespace: es.Namespace, Name: es.Name})
	}
	if _, ok := idx.endpointSlicesByService[svc]; !ok {
		idx.endpointSlicesByService[svc] = map[string]*discovery.EndpointSlice{}
	}
	k := fmt.Sprintf("%s/%s", es.Namespace, es.Name)
	logc := logrus.WithFields(logrus.Fields{"slice": k, "svc": svc})

	cached := idx.endpointSlices[k]
	if ipSets, ok := idx.activeIPSetsByService[svc]; ok {
		logc.Debug("EndpointSlice belongs to an active service")

		// Service contributing these endpoints is active. We need to determine
		// if any endpoints have changed, and if so send through membership updates.
		for _, ipSet := range ipSets {
			idx.maybeReportLive()
			newIPSetContribution := idx.membersFromEndpointSlice(es, ipSet.IncludePorts())
			oldIPSetContributions := idx.membersFromEndpointSlice(cached, ipSet.IncludePorts())

			idx.replaceMembers(ipSet, oldIPSetContributions, newIPSetContribution)
		}
	} else {
		logc.Debugf("EndpointSlice doesn't belong to an active service, simply cache it.")
	}

	// Update caches with the slice.
	idx.endpointSlicesByService[svc][k] = es
	idx.endpointSlices[k] = es
}

func (idx *ServiceIndex) DeleteEndpointSlice(key model.ResourceKey) {
	// k is the namespaced name for identifying the endpoint slice uniquely.
	k := fmt.Sprintf("%s/%s", key.Namespace, key.Name)

	// Check if this is an endpoint slice we know about. If not, there's nothing to do.
	es, ok := idx.endpointSlices[k]
	if !ok {
		return
	}

	// Determine the service that contributed this endpoint slice.
	svc := serviceName(es)
	if ipSets, ok := idx.activeIPSetsByService[svc]; ok {
		for _, ipSet := range ipSets {
			idx.maybeReportLive()
			// Active service has had an EndpointSlice deleted. Iterate all the ip set members
			// contributed by this endpoint slice and decref them. For those which go from 1 to 0,
			// we should send a membership removal from the data plane.
			oldContributions := idx.membersFromEndpointSlice(es, ipSet.IncludePorts())
			idx.replaceMembers(ipSet, oldContributions, nil)
		}
	}

	// Update caches to reflect the deletion.
	delete(idx.endpointSlicesByService[svc], k)
	if len(idx.endpointSlicesByService[svc]) == 0 {
		delete(idx.endpointSlicesByService, svc)
	}
	delete(idx.endpointSlices, k)
}

// A tuple may be shared by multiple slices. Add before removing to avoid
// withdrawing unchanged entries during a refresh.
func (idx *ServiceIndex) replaceMembers(ipSet *ipSetData, oldMembers, newMembers []ipsetmember.IPSetMember) {
	for _, member := range newMembers {
		// Incref all the new members.  If any of them go from 0 to 1 reference then we
		// know that they're new.  We'll temporarily double-count members that were already
		// present, then decref them below.
		//
		// This reference counting also allows us to tolerate duplicate members in the
		// input data.
		idx.maybeReportLive()
		refCount := ipSet.memberToRefCount[member] + 1
		if refCount == 1 {
			idx.OnMemberAdded(ipSet.ID, member)
		}
		ipSet.memberToRefCount[member] = refCount
	}

	// Decref all the old members.  If they hit 0 references, then the member has been
	// removed so we emit an event.
	for _, oldMember := range oldMembers {
		newRefCount := ipSet.memberToRefCount[oldMember] - 1
		if newRefCount == 0 {
			idx.OnMemberRemoved(ipSet.ID, oldMember)
			delete(ipSet.memberToRefCount, oldMember)
		} else {
			ipSet.memberToRefCount[oldMember] = newRefCount
		}
	}
}

func serviceName(es *discovery.EndpointSlice) string {
	svc := es.Labels["kubernetes.io/service-name"]
	name := fmt.Sprintf("%s/%s", es.Namespace, svc)
	logrus.Debugf("Endpoint slice %s belongs to service %s", es.Name, name)
	return name
}

func (idx *ServiceIndex) membersFromEndpointSlice(es *discovery.EndpointSlice, includePorts bool) []ipsetmember.IPSetMember {
	if es == nil {
		// A nil endpoint slice produces no IP set members.
		return nil
	}

	members := []ipsetmember.IPSetMember{}
	if includePorts {
		for _, ep := range es.Endpoints {
			// Create a member for each endpoint + port combination. If there
			// are no ports specified, it means no ports (thus, no IP set membership). If nil is specified,
			// it means ALL ports.
			for _, port := range es.Ports {
				// If the port number is nil, ports are not restricted and left
				// to be interpreted by the context of the consumer. In our case, we will consider
				// a lack of port to mean no IP set membership.
				if port.Port != nil {
					for _, addr := range ep.Addresses {
						ipAddr := ip.FromString(addr)
						if ipAddr == nil {
							// Endpoints can contain domains.
							logrus.Debug("Skipping non-IP endpoint address.")
							continue
						}

						// Determine the protocol for the member. Assume TCP
						// unless the protocol is specified to be something else.
						proto := ipsetmember.ProtocolTCP
						if port.Protocol != nil {
							switch *port.Protocol {
							case v1.ProtocolUDP:
								proto = ipsetmember.ProtocolUDP
							case v1.ProtocolSCTP:
								proto = ipsetmember.ProtocolSCTP
							}
						}
						members = append(members, ipsetmember.MakeIPPortProto(
							ipAddr,
							uint16(*port.Port),
							proto,
						))
					}
				}
			}
		}
	} else {
		for _, ep := range es.Endpoints {
			// Create members for each endpoint with just the cidr. These
			// are used in rules where the protocol and port are already set.
			for _, addr := range ep.Addresses {
				cidr, err := ip.ParseCIDROrIP(addr)
				if err != nil {
					// Endpoints can contain domains.
					logrus.WithError(err).Debug("Skipping non-IP endpoint address.")
					continue
				}

				members = append(members, ipsetmember.MakeCIDROrIPOnly(cidr))
			}
		}
	}
	if includePorts && idx.services != nil {
		members = append(members, clusterIPMembers(idx.services[serviceName(es)], es)...)
	}
	return members
}

func (idx *ServiceIndex) UpdateIPSet(id string, serviceName string) {
	logc := logrus.WithFields(logrus.Fields{"id": id, "service": serviceName})
	if curr, ok := idx.activeIPSetsByID[id]; !ok {
		// No existing entry - this is a new IP set.
		logc.Debugf("New IP set")
	} else if curr.ServiceName == serviceName {
		// Not a new IP set - we're already tracking it as an active service-based
		// IP set, so nothing to do.
		logc.Debugf("Known IP set, nothing to do")
		return
	} else {
		// This branch means that a new service name has generated the same ID as another.
		// This branch is not possible because the ID is uniquely generated from the service name.
		logrus.Panicf("BUG: Same ID generated for two service names: %s and %s", curr.ServiceName, serviceName)
	}

	// New active service IP set.
	as := &ipSetData{
		ID:               id,
		ServiceName:      serviceName,
		memberToRefCount: map[ipsetmember.IPSetMember]uint64{},
	}
	idx.activeIPSetsByID[id] = as
	if _, ok := idx.activeIPSetsByService[serviceName]; !ok {
		idx.activeIPSetsByService[serviceName] = make(map[string]*ipSetData)
	}
	idx.activeIPSetsByService[serviceName][id] = as
	logc.Debugf("Active service IP set")

	// We need to scan for possible updates to the IP set membership. Check endpoint slices for this
	// service to determine endpoints to contribute.
	for _, eps := range idx.endpointSlicesByService[serviceName] {
		members := idx.membersFromEndpointSlice(eps, as.IncludePorts())
		idx.replaceMembers(as, nil, members)
	}
}

func (idx *ServiceIndex) DeleteIPSet(id string) {
	as := idx.activeIPSetsByID[id]
	if as == nil {
		logrus.WithField("id", id).Warning("Delete of unknown IP set, ignoring")
		return
	}
	logrus.Debugf("Deleting service IP set: %s: %s", id, as.ServiceName)

	// Emit events for all the removed CIDRs.
	for member := range as.memberToRefCount {
		if logrus.GetLevel() >= logrus.DebugLevel {
			logrus.WithField("member", member).Debug("Emitting deletion event.")
		}
		idx.OnMemberRemoved(id, member)
	}

	delete(idx.activeIPSetsByID, id)
	delete(idx.activeIPSetsByService[as.ServiceName], id)
	if len(idx.activeIPSetsByService[as.ServiceName]) == 0 {
		delete(idx.activeIPSetsByService, as.ServiceName)
	}
}

func (idx *ServiceIndex) maybeReportLive() {
	// We report from some tight loops so rate limit our reports.
	if time.Since(idx.lastLiveReport) < 100*time.Millisecond {
		return
	}
	idx.OnAlive()
	idx.lastLiveReport = time.Now()
}

// ipSetData represents an active service and state regarding its
// known members.
type ipSetData struct {
	ServiceName string
	ID          string

	// memberToRefCount tracks the count of each member. We need to reference count because
	// it's possible for a given IP set member may exist in more than one EndpointSlice. The reference
	// count lets us properly detect when a member is new or has been deleted.
	memberToRefCount map[ipsetmember.IPSetMember]uint64
}

func (isd *ipSetData) IncludePorts() bool {
	return !strings.HasPrefix(isd.ID, "svcnoport")
}

func (idx *ServiceIndex) updateService(name string, svc *v1.Service) {
	old := idx.services[name]
	if svc == nil {
		delete(idx.services, name)
	} else {
		idx.services[name] = svc
	}
	for _, set := range idx.activeIPSetsByService[name] {
		if !set.IncludePorts() {
			continue
		}
		for _, slice := range idx.endpointSlicesByService[name] {
			idx.replaceMembers(set, clusterIPMembers(old, slice), clusterIPMembers(svc, slice))
		}
	}
}

// Outbound Windows switch ACLs see a ClusterIP and frontend port before DNAT.
// Restrict frontend tuples to the address family and named ports of a backing
// slice. ExternalIPs and LoadBalancer addresses are not ClusterIPs.
func clusterIPMembers(svc *v1.Service, slice *discovery.EndpointSlice) []ipsetmember.IPSetMember {
	if svc == nil || svc.Spec.Type == v1.ServiceTypeExternalName || len(slice.Endpoints) == 0 {
		return nil
	}
	type portKey struct {
		name     string
		protocol v1.Protocol
	}
	backendPorts := make(map[portKey]bool, len(slice.Ports))
	for _, p := range slice.Ports {
		if p.Port == nil {
			continue
		}
		name := ""
		if p.Name != nil {
			name = *p.Name
		}
		protocol := v1.ProtocolTCP
		if p.Protocol != nil {
			protocol = *p.Protocol
		}
		backendPorts[portKey{name, protocol}] = true
	}
	var members []ipsetmember.IPSetMember
	ips := svc.Spec.ClusterIPs
	if len(ips) == 0 {
		ips = []string{svc.Spec.ClusterIP}
	}
	for _, value := range ips {
		addr := ip.FromString(value)
		if addr == nil {
			continue
		}
		if (addr.Version() == 4 && slice.AddressType != discovery.AddressTypeIPv4) ||
			(addr.Version() == 6 && slice.AddressType != discovery.AddressTypeIPv6) {
			continue
		}
		for _, p := range svc.Spec.Ports {
			protocol := p.Protocol
			if protocol == "" {
				protocol = v1.ProtocolTCP
			}
			if !backendPorts[portKey{p.Name, protocol}] {
				continue
			}
			number := ipsetmember.ProtocolTCP
			switch protocol {
			case v1.ProtocolTCP:
			case v1.ProtocolUDP:
				number = ipsetmember.ProtocolUDP
			case v1.ProtocolSCTP:
				number = ipsetmember.ProtocolSCTP
			default:
				continue
			}
			members = append(members, ipsetmember.MakeIPPortProto(addr, uint16(p.Port), number))
		}
	}
	return members
}
