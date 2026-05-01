// Copyright (c) 2024 Tigera, Inc. All rights reserved.
//
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

package windows

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	apiv3 "github.com/projectcalico/api/pkg/apis/projectcalico/v3"
	"github.com/sirupsen/logrus"
)

// HNSSubnet mirrors hcsshim.Subnet without the hcsshim dependency.
type HNSSubnet struct {
	AddressPrefix  string
	GatewayAddress string
}

// HNSNetworkInfo mirrors the fields of hcsshim.HNSNetwork used by our logic.
type HNSNetworkInfo struct {
	Id      string
	Name    string
	Type    string
	Subnets []HNSSubnet
}

// HNSEndpointInfo mirrors the fields of hcsshim.HNSEndpoint used by our logic.
type HNSEndpointInfo struct {
	Id             string
	Name           string
	VirtualNetwork string
	IPAddress      net.IP
}

// HNSNetworkAPI abstracts HNS network operations for testing.
type HNSNetworkAPI interface {
	GetByName(name string) (*HNSNetworkInfo, error)
	Delete(network *HNSNetworkInfo) error
	Create(jsonRequest string) (*HNSNetworkInfo, error)
}

// HNSEndpointAPI abstracts HNS endpoint operations for testing.
type HNSEndpointAPI interface {
	GetByName(name string) (*HNSEndpointInfo, error)
	Delete(endpoint *HNSEndpointInfo) error
	Create(jsonRequest string) (*HNSEndpointInfo, error)
	HostAttach(endpoint *HNSEndpointInfo, compartmentID uint16) error
}

// getNthIP returns the network address of PodCIDR offset by n. Carries
// across byte boundaries, so it works correctly for any n that fits in
// the address family (n up to 2^32-1 for IPv4, 2^64-1+ for IPv6 in
// practice limited by uint64 here, which is enough for /64 carving).
//
// Used for picking the gateway (n=1) and host endpoint (n=2) within a
// pod block, plus tests that exercise carry across larger n.
func getNthIP(PodCIDR *net.IPNet, n int) net.IP {
	ip := PodCIDR.IP
	add := uint64(n)
	if v4 := ip.To4(); v4 != nil {
		buf := make([]byte, 4)
		copy(buf, v4)
		var carry uint64 = add
		for i := 3; i >= 0 && carry > 0; i-- {
			sum := uint64(buf[i]) + (carry & 0xff)
			buf[i] = byte(sum & 0xff)
			carry = (carry >> 8) + (sum >> 8)
		}
		return buf
	}
	buf := make([]byte, 16)
	copy(buf, ip.To16())
	var carry uint64 = add
	for i := 15; i >= 0 && carry > 0; i-- {
		sum := uint64(buf[i]) + (carry & 0xff)
		buf[i] = byte(sum & 0xff)
		carry = (carry >> 8) + (sum >> 8)
	}
	return buf
}

// networkNeedsRecreate checks whether an existing HNS network can satisfy
// the requested IPv4 (and optional IPv6) configuration. Returns true when
// the network must be recreated.
//
// Asymmetric semantics: the caller (CNI plugin) is invoked per-pod, and
// pods do not all request the same address families. The function MUST
// NOT trigger a recreate just because the existing network has more
// subnets than the current pod needs. Specifically, an IPv4-only pod's
// CNI invocation (subNetV6==nil) must reuse a dual-stack network as-is,
// because deleting it would destroy every running dual-stack pod's HNS
// endpoint.
//
// Stripping IPv6 from the node network entirely is operator-driven via
// FELIX_IPV6SUPPORT=false at calico-node startup, not a per-pod CNI
// decision.
//
// Recreate is required in these cases:
//   - existing has no matching IPv4 prefix/gateway (subV4 changed)
//   - subNetV6 != nil and existing does not contain a matching IPv6 entry
//     (network needs to gain IPv6, or the IPv6 prefix rotated via DHCPv6-PD)
//
// Recreate is NOT triggered when:
//   - subNetV6 == nil and existing already has matching IPv4 (extra IPv6
//     entries are tolerated)
//   - subNetV6 == nil and existing has matching IPv4 plus extra unrelated
//     IPv4 subnets (don't tear down on per-pod CNI invocations)
//   - both prefixes match exactly, regardless of subnet ordering
func networkNeedsRecreate(existingSubnets []HNSSubnet, subNet *net.IPNet, subNetV6 *net.IPNet) bool {
	v4Prefix := subNet.String()
	v4GW := getNthIP(subNet, 1).String()

	v4Found := false
	for _, s := range existingSubnets {
		if s.AddressPrefix == v4Prefix && s.GatewayAddress == v4GW {
			v4Found = true
			break
		}
	}
	if !v4Found {
		return true
	}

	if subNetV6 == nil {
		// IPv4-only request. We don't care whether the existing network
		// has additional IPv6 subnets — they don't break this pod, and
		// recreating to remove them would break other dual-stack pods.
		return false
	}

	v6Prefix := subNetV6.String()
	v6GW := getNthIP(subNetV6, 1).String()
	for _, s := range existingSubnets {
		if s.AddressPrefix == v6Prefix && s.GatewayAddress == v6GW {
			return false
		}
	}
	return true
}

// ensureNetworkExistsWithAPI creates or validates the Calico HNS L2Bridge network.
func ensureNetworkExistsWithAPI(networkName string, subNet *net.IPNet, subNetV6 *net.IPNet, logger *logrus.Entry, api HNSNetworkAPI) (*HNSNetworkInfo, error) {
	var err error
	createNetwork := true

	hnsNetwork, _ := api.GetByName(networkName)
	if hnsNetwork != nil {
		if !networkNeedsRecreate(hnsNetwork.Subnets, subNet, subNetV6) {
			createNetwork = false
			logger.Infof("Found existing HNS network [%+v]", hnsNetwork)
		}
	}

	if createNetwork {
		if hnsNetwork != nil {
			// The network exists but subnets don't match (e.g. IPv4-only
			// but dual-stack requested, or IPv6 prefix changed via
			// DHCPv6-PD).  L2Bridge subnets can't be modified dynamically
			// (microsoft/hcsshim#786), so delete and recreate.  This
			// disrupts existing pods, but they will be rescheduled with
			// correct IPs from the new prefix.
			logger.Warnf("HNS network %s exists but subnets do not match desired config. "+
				"Deleting and recreating network; existing pods will be disrupted.", networkName)
		}
	}

	if createNetwork {
		// Clean up ALL L2Bridge networks before creating ours.
		// The "External" placeholder from node-service.ps1 or a stale
		// "Calico" from a concurrent restart can hold the adapter.
		for _, name := range []string{"External", networkName} {
			if n, _ := api.GetByName(name); n != nil && n.Type == "L2Bridge" {
				logger.Infof("Removing L2Bridge network %q to free the physical adapter", name)
				if err := api.Delete(n); err != nil {
					logger.WithError(err).Warnf("Failed to delete %q network", name)
				}
			}
		}
		// Wait for the adapter to become available after deleting networks.
		time.Sleep(10 * time.Second)

		addressPrefix := subNet.String()
		gatewayAddress := getNthIP(subNet, 1)

		subnets := []interface{}{
			map[string]interface{}{
				"AddressPrefix":  addressPrefix,
				"GatewayAddress": gatewayAddress.String(),
			},
		}
		if subNetV6 != nil {
			gwV6 := getNthIP(subNetV6, 1)
			subnets = append(subnets, map[string]interface{}{
				"AddressPrefix":  subNetV6.String(),
				"GatewayAddress": gwV6.String(),
			})
		}

		req := map[string]interface{}{
			"Name":    networkName,
			"Type":    "L2Bridge",
			"Subnets": subnets,
		}
		if subNetV6 != nil {
			req["IPv6"] = true
		}

		reqStr, err := json.Marshal(req)
		if err != nil {
			logger.Errorf("Error in converting to json format")
			return nil, err
		}

		logger.Infof("Attempting to create HNS network, request: %v", string(reqStr))
		var createErr error
		for attempt := 0; attempt < 10; attempt++ {
			hnsNetwork, createErr = api.Create(string(reqStr))
			if createErr == nil {
				break
			}
			delay := time.Duration(3*(attempt+1)) * time.Second
			if delay > 10*time.Second {
				delay = 10 * time.Second
			}
			logger.WithError(createErr).Warnf("HNS network creation attempt %d/10 failed, retrying in %v", attempt+1, delay)
			time.Sleep(delay)
		}
		if createErr != nil {
			logger.Errorf("unable to create network [%v] after retries, error: %v", networkName, createErr)
			return nil, createErr
		}
		logger.Infof("Created HNS network [%v] as %+v", networkName, hnsNetwork)
	}
	return hnsNetwork, err
}

// createAndAttachHostEPWithAPI creates (or reuses) the host endpoint on an HNS network.
func createAndAttachHostEPWithAPI(epName string, hnsNetwork *HNSNetworkInfo, subNet *net.IPNet, logger *logrus.Entry, api HNSEndpointAPI) (*HNSEndpointInfo, error) {
	var err error
	endpointAddress := getNthIP(subNet, 2)
	attachEndpoint := true

	hnsEndpoint, _ := api.GetByName(epName)
	if hnsEndpoint != nil {
		if !hnsEndpoint.IPAddress.Equal(endpointAddress) {
			if err = api.Delete(hnsEndpoint); err != nil {
				logger.Errorf("Unable to delete existing bridge endpoint [%v], error: %v", epName, err)
				return nil, err
			}
			logger.Infof("Deleted stale bridge endpoint [%v]", epName)
			hnsEndpoint = nil
		} else if strings.ToUpper(hnsEndpoint.VirtualNetwork) == strings.ToUpper(hnsNetwork.Id) {
			attachEndpoint = false
		} else {
			logger.Errorf("HnsEndpoint virtual network %s not matching ID %s",
				hnsEndpoint.VirtualNetwork, hnsNetwork.Id)
		}
	}

	if hnsEndpoint == nil {
		epReq := map[string]interface{}{
			"Name":           epName,
			"IPAddress":      endpointAddress.String(),
			"VirtualNetwork": hnsNetwork.Id,
		}

		for _, subnet := range hnsNetwork.Subnets {
			_, sn, err := net.ParseCIDR(subnet.AddressPrefix)
			if err == nil && sn.IP.To4() == nil {
				epReq["IPv6Address"] = getNthIP(sn, 2).String()
				logger.Infof("Setting host endpoint IPv6 address to %s", epReq["IPv6Address"])
				break
			}
		}

		epJSON, _ := json.Marshal(epReq)
		logger.Infof("Attempting to create bridge endpoint [%s]", string(epJSON))
		var created *HNSEndpointInfo
		created, err = api.Create(string(epJSON))
		if err != nil {
			logger.Errorf("Unable to create bridge endpoint [%v], error: %v", epName, err)
			return nil, err
		}
		hnsEndpoint = created
		logger.Infof("Created bridge endpoint [%v] as %+v", epName, hnsEndpoint)
	}

	if attachEndpoint {
		if err = api.HostAttach(hnsEndpoint, 1); err != nil {
			logger.Errorf("Unable to hot attach bridge endpoint [%v] to host compartment, error: %v", epName, err)
			return nil, err
		}
		logger.Infof("Attached bridge endpoint [%v] to host", epName)
	}
	return hnsEndpoint, err
}

// filterIPAMPools filters out IPv6 pools (which HNS rejects in OutBoundNAT)
// and determines whether NAT outgoing should be enabled based on which pool
// contains the pod's IP.
func filterIPAMPools(pools []apiv3.IPPool, podIP net.IP) (cidrs []*net.IPNet, natOutgoing bool) {
	natOutgoing = true
	for _, pool := range pools {
		_, cidr, err := net.ParseCIDR(pool.Spec.CIDR)
		if err != nil {
			continue
		}
		// Skip IPv6 CIDRs: HNS on Windows rejects OutBoundNAT policies
		// containing IPv6 CIDRs.
		if cidr.IP.To4() == nil {
			continue
		}
		cidrs = append(cidrs, cidr)
		if cidr.Contains(podIP) {
			natOutgoing = pool.Spec.NATOutgoing
		}
	}
	return cidrs, natOutgoing
}

// CreateNetworkName builds the network name from the given prefix and subnet.
func CreateNetworkName(netName string, subnet *net.IPNet) string {
	str := subnet.IP.String()
	network := strings.Replace(str, ".", "-", -1)
	name := netName + "-" + network
	return name
}

// mustParseCIDR is a test helper that parses a CIDR or panics.
func mustParseCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(fmt.Sprintf("mustParseCIDR(%q): %v", s, err))
	}
	return n
}
