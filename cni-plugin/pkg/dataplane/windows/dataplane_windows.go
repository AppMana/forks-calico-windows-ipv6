// Copyright (c) 2018-2021 Tigera, Inc. All rights reserved.
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
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Microsoft/hcsshim"
	"github.com/Microsoft/hcsshim/hcn"
	"github.com/buger/jsonparser"
	"github.com/containernetworking/cni/pkg/skel"
	cniv1 "github.com/containernetworking/cni/pkg/types/100"
	"github.com/containernetworking/plugins/pkg/hns"
	"github.com/juju/clock"
	"github.com/juju/errors"
	"github.com/juju/mutex"
	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/projectcalico/calico/cni-plugin/internal/pkg/utils/cri"
	"github.com/projectcalico/calico/cni-plugin/internal/pkg/utils/winpol"
	"github.com/projectcalico/calico/cni-plugin/pkg/types"
	api "github.com/projectcalico/calico/libcalico-go/lib/apis/v3"
	calicoclient "github.com/projectcalico/calico/libcalico-go/lib/clientv3"
	"github.com/projectcalico/calico/libcalico-go/lib/options"
	"github.com/projectcalico/calico/libcalico-go/lib/winutils"
)

const (
	DefaultVNI = 4096
)

type windowsDataplane struct {
	conf   types.NetConf
	logger *logrus.Entry
}

func NewWindowsDataplane(conf types.NetConf, logger *logrus.Entry) *windowsDataplane {
	return &windowsDataplane{
		conf:   conf,
		logger: logger,
	}
}

func loadNetConf(bytes []byte) (*hns.NetConf, string, error) {
	n := &hns.NetConf{}
	if err := json.Unmarshal(bytes, n); err != nil {
		return nil, "", fmt.Errorf("failed to load netconf: %v", err)
	}
	return n, n.CNIVersion, nil
}

func acquireLock() (mutex.Releaser, error) {
	spec := mutex.Spec{
		Name:    "TigeraCalicoCNINetworkMutex",
		Clock:   clock.WallClock,
		Delay:   50 * time.Millisecond,
		Timeout: 90000 * time.Millisecond,
	}
	logrus.Infof("Trying to acquire lock %v", spec)
	m, err := mutex.Acquire(spec)
	if err != nil {
		logrus.Errorf("Error acquiring lock %v", spec)
		return nil, err
	}
	logrus.Infof("Acquired lock %v", spec)
	return m, nil
}

func SetupL2bridgeNetwork(networkName string, subNet *net.IPNet, subNetV6 *net.IPNet, mgmtIP, mgmtIPv6 string, logger *logrus.Entry) (*hcsshim.HNSNetwork, error) {
	hnsNetwork, err := EnsureNetworkExists(networkName, subNet, subNetV6, mgmtIP, mgmtIPv6, logger)
	if err != nil {
		logger.Errorf("Unable to create hns network %s", networkName)
		return nil, err
	}

	// Create host hns endpoint
	epName := networkName + "_ep"
	hnsEndpoint, err := CreateAndAttachHostEP(epName, hnsNetwork, subNet, logger)
	if err != nil {
		logger.Errorf("Unable to create host hns endpoint %s", epName)
		return nil, err
	}

	// Check for management ip getting assigned to the network, interface with the management ip
	// and then enable forwarding on management interface as well as endpoint.
	// Update the hnsNetwork variable with management ip
	hnsNetwork, err = chkMgmtIPandEnableForwarding(networkName, hnsEndpoint, logger)
	if err != nil {
		logger.Errorf("Failed to enable forwarding : %v", err)
		return nil, err
	}

	return hnsNetwork, err
}

func SetupVxlanNetwork(networkName string, subNet *net.IPNet, vni uint64, logger *logrus.Entry) (*hcsshim.HNSNetwork, error) {
	hnsNetwork, err := ensureVxlanNetworkExists(networkName, subNet, vni, logger)
	if err != nil {
		logger.Errorf("Unable to create hns network %s", networkName)
		return nil, err
	}

	// Create host hns endpoint
	epName := networkName + "_ep"
	_, err = createAndAttachVxlanHostEP(epName, hnsNetwork, subNet, logger)
	if err != nil {
		logger.Errorf("Unable to create host hns endpoint %s", epName)
		return nil, err
	}

	return hnsNetwork, err
}

// DoNetworking performs the networking for the given config and IPAM result
func (d *windowsDataplane) DoNetworking(
	ctx context.Context,
	calicoClient calicoclient.Interface,
	args *skel.CmdArgs,
	result *cniv1.Result,
	desiredVethName string,
	routes []*net.IPNet,
	endpoint *api.WorkloadEndpoint,
	annotations map[string]string,
) (hostVethName, contVethMAC string, err error) {
	hostVethName = desiredVethName
	if len(routes) > 0 {
		logrus.WithField("routes", routes).Debug("Ignoring in-container routes; not supported on Windows.")
	}

	// Extract IPv4 and IPv6 addresses from the IPAM result.
	var podIPv4, podIPv6 net.IP
	var subNetV4, subNetV6 *net.IPNet
	for _, ipConf := range result.IPs {
		ip, subnet, _ := net.ParseCIDR(ipConf.Address.String())
		if ip.To4() != nil {
			podIPv4 = ip
			subNetV4 = subnet
		} else {
			podIPv6 = ip
			subNetV6 = subnet
		}
	}
	// Fall back to the first IP for legacy compatibility.
	podIP := podIPv4
	subNet := subNetV4
	if podIP == nil {
		podIP, subNet, _ = net.ParseCIDR(result.IPs[0].Address.String())
	}

	n, _, err := loadNetConf(args.StdinData)
	if err != nil {
		d.logger.Errorf("Error loading args")
		return "", "", err
	}

	// Assigning DNS details read from RuntimeConfig or cni.conf to result
	// If DNS details is present in the RuntimeConfig, then DNS details of RuntimeConfig will take precedence over cni.conf DNS
	if len(d.conf.RuntimeConfig.DNS.Nameservers) >= 1 {
		result.DNS.Nameservers = d.conf.RuntimeConfig.DNS.Nameservers
		result.DNS.Domain = d.conf.RuntimeConfig.DNS.Domain
		result.DNS.Search = d.conf.RuntimeConfig.DNS.Search
		result.DNS.Options = d.conf.RuntimeConfig.DNS.Options
	} else {
		result.DNS = n.DNS
	}

	// We need to know the IPAM pools to program the correct NAT exclusion list.  Look those up
	// before we take the global lock.
	allIPAMPools, natOutgoing, err := lookupIPAMPools(ctx, podIP, calicoClient)
	if err != nil {
		d.logger.WithError(err).Error("Failed to look up IPAM pools")
		return "", "", err
	}

	// Acquire mutex lock
	m, err := acquireLock()
	if err != nil {
		d.logger.Errorf("Unable to acquire lock")
		return "", "", err
	}
	defer m.Release()

	// Create hns network
	var networkName string
	if d.conf.WindowsUseSingleNetwork {
		d.logger.WithField("name", d.conf.Name).Info(
			"Overriding network name, only a single IPAM block will be supported on this host")
		networkName = d.conf.Name
	} else {
		networkName = CreateNetworkName(n.Name, subNet)
	}

	var hnsNetwork *hcsshim.HNSNetwork
	if d.conf.Mode == "vxlan" {
		hnsNetwork, err = SetupVxlanNetwork(networkName, subNet, d.conf.VXLANVNI, d.logger)
	} else {
		// Pin HNS ManagementIP / ManagementIPv6 to whatever Calico's IP
		// autodetection picked for this node. This is the same address
		// BIRD/Felix/confd use as the BGP source. Without this, HNS
		// silently auto-selects the first address on the underlying NIC
		// (typically a SLAAC GUA), and VFP drops NS for any other host
		// IPv6 — including the ULA we want to use for stable BGP across
		// DHCPv6-PD prefix rotations. Failure to read the node spec is
		// non-fatal; we fall back to legacy behaviour.
		mgmtIP, mgmtIPv6 := lookupNodeBGPIPs(ctx, calicoClient, d.logger)
		hnsNetwork, err = SetupL2bridgeNetwork(networkName, subNet, subNetV6, mgmtIP, mgmtIPv6, d.logger)
	}
	if err != nil {
		d.logger.Errorf("Unable to create hns network %s", networkName)
		return "", "", err
	}

	// Create endpoint for container
	hnsEndpointCont, hcsEndpoint, err := d.createAndAttachContainerEP(args, hnsNetwork, subNet, allIPAMPools, natOutgoing, result, n, podIPv6, subNetV6)
	if err != nil {
		epName := hns.ConstructEndpointName(args.ContainerID, args.Netns, n.Name)
		d.logger.Errorf("Unable to create container hns endpoint %s", epName)
		return "", "", err
	}

	// The Priority is set to the largest value (65500) with which
	// the ACL policy when applied on windows works.
	// The Priority has to be set to a large enough value for the default
	// policy so that if other policies exist then they take precedence.
	if d.conf.WindowsDisableDefaultDenyAllPolicy == false {
		var err error
		if cri.IsDockershimV1(args.Netns) {
			defaultDenyAllACL := &hcsshim.ACLPolicy{
				Id:        "CNIDefaultDenyAllPolicy",
				Type:      hcsshim.ACL,
				RuleType:  hcsshim.Switch,
				Action:    hcsshim.Block,
				Direction: hcsshim.In,
				Protocol:  256,
				Priority:  65500,
			}
			err = hnsEndpointCont.ApplyACLPolicy(defaultDenyAllACL)
		} else {
			aclPolicySettings := hcn.AclPolicySetting{
				RuleType:  hcn.RuleTypeSwitch,
				Action:    hcn.ActionTypeBlock,
				Direction: hcn.DirectionTypeIn,
				Protocols: "256",
				Priority:  65500,
			}

			policyJSON, err := json.Marshal(aclPolicySettings)
			if err != nil {
				d.logger.WithError(err).Error("Failed to marshal ACL policy")
				return "", "", err
			}

			defaultDenyAllACL := hcn.PolicyEndpointRequest{
				Policies: []hcn.EndpointPolicy{
					{
						Type:     hcn.ACL,
						Settings: json.RawMessage(policyJSON),
					},
				},
			}
			err = hcsEndpoint.ApplyPolicy(hcn.RequestTypeUpdate, defaultDenyAllACL)
		}
		if err != nil {
			d.logger.Errorf("Error applying ACL policy DenyAll")
			return "", "", err
		}
	}
	if cri.IsDockershimV1(args.Netns) {
		contVethMAC = hnsEndpointCont.MacAddress
	} else {
		contVethMAC = hcsEndpoint.MacAddress
	}
	return hostVethName, contVethMAC, err
}

func lookupIPAMPools(
	ctx context.Context, podIP net.IP, calicoClient calicoclient.Interface,
) (
	cidrs []*net.IPNet,
	natOutgoing bool,
	err error,
) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	pools, err := calicoClient.IPPools().List(ctx, options.ListOptions{})
	if err != nil {
		return
	}
	cidrs, natOutgoing = filterIPAMPools(pools.Items, podIP)
	return
}

// filterIPAMPools is defined in hns_types.go (cross-platform).

func ensureVxlanNetworkExists(networkName string, subNet *net.IPNet, vni uint64, logger *logrus.Entry) (*hcsshim.HNSNetwork, error) {
	var err error
	createNetwork := true
	expectedAddressPrefix := subNet.String()
	expectedGW := getNthIP(subNet, 1)
	var expectedVNI uint64

	expectedNetwork := &hcsshim.HNSNetwork{
		Name:    networkName,
		Type:    "Overlay",
		Subnets: make([]hcsshim.Subnet, 0, 1),
	}

	if vni == 0 {
		expectedVNI = DefaultVNI
	} else if vni < DefaultVNI {
		return nil, fmt.Errorf("Windows does not support VXLANVNI < 4096")
	} else {
		expectedVNI = vni
	}

	// Checking if HNS network exists
	existingNetwork, _ := hcsshim.GetHNSNetworkByName(networkName)
	if existingNetwork != nil {
		if existingNetwork.Type == expectedNetwork.Type {
			for _, subnet := range existingNetwork.Subnets {
				if subnet.AddressPrefix == expectedAddressPrefix && subnet.GatewayAddress == expectedGW.String() {
					createNetwork = false
					logger.Infof("Found existing HNS network [%+v]", existingNetwork)
					break
				}
			}
		}
	}

	if createNetwork {
		// Delete stale network
		if existingNetwork != nil {
			if _, err := existingNetwork.Delete(); err != nil {
				logger.Errorf("Unable to delete existing network [%v], error: %v", existingNetwork.Name, err)
				return nil, err
			}
			logger.Infof("Deleted stale HNS network [%v]", existingNetwork.Name)
		}

		// Add a VxLan subnet
		expectedNetwork.Subnets = append(expectedNetwork.Subnets, hcsshim.Subnet{
			AddressPrefix:  expectedAddressPrefix,
			GatewayAddress: expectedGW.String(),
			Policies: []json.RawMessage{
				[]byte(fmt.Sprintf(`{"Type":"VSID","VSID":%d}`, expectedVNI)),
			},
		})

		// Config request params
		jsonRequest, err := json.Marshal(expectedNetwork)
		if err != nil {
			return nil, errors.Annotatef(err, "failed to marshal %+v", expectedNetwork)
		}

		logger.Infof("Attempting to create HNSNetwork %s", string(jsonRequest))
		newNetwork, err := hcsshim.HNSNetworkRequest("POST", "", string(jsonRequest))
		if err != nil {
			return nil, errors.Annotatef(err, "failed to create HNSNetwork %s", networkName)
		}

		var waitErr, lastErr error
		// Wait for the network to populate Management IP
		logger.Infof("Waiting to get ManagementIP from HNSNetwork %s", networkName)
		waitErr = wait.Poll(500*time.Millisecond, 5*time.Second, func() (done bool, err error) {
			newNetwork, lastErr = hcsshim.HNSNetworkRequest("GET", newNetwork.Id, "")
			return newNetwork != nil && len(newNetwork.ManagementIP) != 0, nil
		})
		if waitErr == wait.ErrWaitTimeout {
			return nil, errors.Annotatef(lastErr, "timeout, failed to get management IP from HNSNetwork %s", networkName)
		}

		// Wait for the interface with the management IP
		logger.Infof("Waiting to get net interface for HNSNetwork %s (%s)", networkName, newNetwork.ManagementIP)
		waitErr = wait.Poll(500*time.Millisecond, 5*time.Second, func() (done bool, err error) {
			mgmtIP := net.ParseIP(newNetwork.ManagementIP)
			_, lastErr = lookupManagementIface(mgmtIP, logger)
			return lastErr == nil, nil
		})
		if waitErr == wait.ErrWaitTimeout {
			return nil, errors.Annotatef(lastErr, "timeout, failed to get net interface for HNSNetwork %s (%s)", networkName, newNetwork.ManagementIP)
		}

		logger.Infof("Created HNSNetwork %s", networkName)
		existingNetwork = newNetwork
	}

	existingNetworkV2, err := hcn.GetNetworkByID(existingNetwork.Id)
	if err != nil {
		return nil, errors.Annotatef(err, "Could not find vxlan0 in V2")
	}

	addHostRoute := true
	for _, policy := range existingNetworkV2.Policies {
		if policy.Type == hcn.HostRoute {
			addHostRoute = false
		}
	}
	if addHostRoute {
		hostRoutePolicy := hcn.NetworkPolicy{
			Type:     hcn.HostRoute,
			Settings: []byte("{}"),
		}

		networkRequest := hcn.PolicyNetworkRequest{
			Policies: []hcn.NetworkPolicy{hostRoutePolicy},
		}
		err := existingNetworkV2.AddPolicy(networkRequest)
		if err != nil {
			logger.Warnf("Error adding policy to network : %v", err)
		}
	}

	return existingNetwork, nil
}

// networkNeedsRecreate, HNSNetworkAPI, and HNSEndpointAPI are defined in hns_types.go (cross-platform).

// realHNS bridges HNSNetworkAPI to hcsshim.
type realHNS struct{}

func (r *realHNS) GetByName(name string) (*HNSNetworkInfo, error) {
	n, err := hcsshim.GetHNSNetworkByName(name)
	if err != nil {
		return nil, err
	}
	info := hcsshimNetworkToInfo(n)
	// hcsshim's HNSNetwork struct (v0.11.x, v0.14.x) doesn't declare
	// ManagementIPv6 even though HNS itself returns it. Re-issue the
	// GET via raw vmcompute.dll syscall and pull the field out so we
	// can detect a stale ManagementIPv6 and trigger recreate.
	if mgmtV6, err := queryHNSManagementIPv6(n.Id); err == nil {
		info.ManagementIPv6 = mgmtV6
	} else {
		logrus.WithError(err).Debug("Failed to query HNS ManagementIPv6 via raw syscall; recreate-on-mismatch disabled")
	}
	return info, nil
}

func (r *realHNS) Delete(network *HNSNetworkInfo) error {
	n, err := hcsshim.GetHNSNetworkByName(network.Name)
	if err != nil {
		return err
	}
	_, err = n.Delete()
	return err
}

func (r *realHNS) Create(jsonRequest string) (*HNSNetworkInfo, error) {
	n, err := hcsshim.HNSNetworkRequest("POST", "", jsonRequest)
	if err != nil {
		return nil, err
	}
	return hcsshimNetworkToInfo(n), nil
}

// EnsureWeakHost shells out to PowerShell's Set-NetIPInterface to set
// WeakHostReceive / WeakHostSend / Forwarding = Enabled on the
// management vNIC and the L2Bridge endpoint vNIC, for both IPv4 and
// IPv6. HNS resets these every time the L2Bridge is (re)created, so
// this must run on every successful create.
//
// The vNIC names follow Calico's naming convention:
//   - vEthernet (Ethernet) — the host's management vNIC bound to the
//     physical NIC by the Hyper-V vSwitch.
//   - vEthernet (Calico_ep) — the host endpoint of the L2Bridge HNS
//     network (named "<network>_ep"; we use "Calico" so it's "Calico_ep").
//
// We tolerate "InterfaceAlias not found" because the Calico_ep vNIC
// only exists once the host endpoint has been programmed. The caller
// invokes EnsureWeakHost from ensureNetworkExistsWithAPI, which runs
// before the host endpoint exists on the FIRST call after a wipe;
// subsequent calls (per-pod) will reconcile correctly.
func (r *realHNS) EnsureWeakHost(logger *logrus.Entry) error {
	// Single PowerShell roundtrip — cheaper than 4-8 separate calls
	// and idempotent. Errors per (vNIC, AF) tuple are logged but
	// non-fatal so a stale Calico_ep doesn't block a fresh Ethernet
	// reconcile.
	cmd := `
		$ifs = @('vEthernet (Ethernet)','vEthernet (Calico_ep)')
		foreach ($if in $ifs) {
			foreach ($af in @('IPv4','IPv6')) {
				try {
					Set-NetIPInterface -InterfaceAlias $if -AddressFamily $af ` +
		`-WeakHostReceive Enabled -WeakHostSend Enabled -Forwarding Enabled -ErrorAction Stop
					Write-Host ("EnsureWeakHost: " + $if + "/" + $af + " -> Enabled")
				} catch {
					Write-Host ("EnsureWeakHost: " + $if + "/" + $af + " skipped: " + $_.Exception.Message)
				}
			}
		}`
	stdout, stderr, err := winutils.Powershell(cmd)
	if err != nil {
		return errors.Annotatef(err, "EnsureWeakHost: powershell (stderr=%q)", stderr)
	}
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			logger.Info(line)
		}
	}
	return nil
}

// StripNonDesiredHostIPv6 removes RA-derived IPv6 addresses on
// vEthernet (Ethernet*) that don't equal mgmtIPv6 (and aren't link-
// local). Called immediately before HNS L2Bridge create so HNS's
// async NIC scan picks mgmtIPv6 as ManagementIPv6.
//
// SLAAC will re-add the stripped addresses on the next RA — by then
// HNS has pinned the desired address. Failures are logged not fatal.
func (r *realHNS) StripNonDesiredHostIPv6(mgmtIPv6 string, logger *logrus.Entry) error {
	if mgmtIPv6 == "" {
		return nil
	}
	cmd := `
		$desired = '` + mgmtIPv6 + `'
		$candidates = Get-NetIPAddress -AddressFamily IPv6 -ErrorAction SilentlyContinue |
			Where-Object { $_.InterfaceAlias -like 'vEthernet (Ethernet*' -and
			               $_.IPAddress -notlike 'fe80*' -and
			               $_.PrefixOrigin -eq 'RouterAdvertisement' -and
			               $_.IPAddress -ne $desired }
		foreach ($c in $candidates) {
			try {
				Remove-NetIPAddress -InterfaceIndex $c.InterfaceIndex -IPAddress $c.IPAddress -Confirm:$false -ErrorAction Stop
				Write-Host ("StripNonDesiredHostIPv6: removed " + $c.IPAddress)
			} catch {
				Write-Host ("StripNonDesiredHostIPv6: WARNING: " + $c.IPAddress + " " + $_.Exception.Message)
			}
		}`
	stdout, stderr, err := winutils.Powershell(cmd)
	if err != nil {
		return errors.Annotatef(err, "StripNonDesiredHostIPv6: powershell (stderr=%q)", stderr)
	}
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			logger.Info(line)
		}
	}
	return nil
}

var defaultHNS HNSNetworkAPI = &realHNS{}

func hcsshimNetworkToInfo(n *hcsshim.HNSNetwork) *HNSNetworkInfo {
	info := &HNSNetworkInfo{
		Id:           n.Id,
		Name:         n.Name,
		Type:         n.Type,
		ManagementIP: n.ManagementIP,
		Subnets:      make([]HNSSubnet, len(n.Subnets)),
	}
	for i, s := range n.Subnets {
		info.Subnets[i] = HNSSubnet{
			AddressPrefix:  s.AddressPrefix,
			GatewayAddress: s.GatewayAddress,
		}
	}
	return info
}

// EnsureNetworkExists creates or validates the Calico HNS L2Bridge network.
// It handles three scenarios:
//   - No existing network: create new (with optional IPv6 subnet).
//   - Existing network with matching subnets: reuse as-is.
//   - Existing network with mismatched subnets (e.g. IPv4-only but dual-stack
//     requested): log a warning and keep the existing network. L2Bridge subnets
//     cannot be modified dynamically (microsoft/hcsshim#786) and deleting the
//     network tears down the vSwitch. A node reboot is required to transition.
//
// When creating a new network, it also removes the placeholder "External"
// L2Bridge created by node-service.ps1, since only one L2Bridge can bind the
// physical adapter.
//
// ensureNetworkExistsWithAPI (cross-platform) lives in hns_types.go.
// This wrapper converts the result back to hcsshim for downstream callers.
func EnsureNetworkExists(networkName string, subNet *net.IPNet, subNetV6 *net.IPNet, mgmtIP, mgmtIPv6 string, logger *logrus.Entry) (*hcsshim.HNSNetwork, error) {
	info, err := ensureNetworkExistsWithAPI(networkName, subNet, subNetV6, mgmtIP, mgmtIPv6, logger, defaultHNS)
	if err != nil {
		return nil, err
	}
	// Re-fetch from hcsshim to get the full HNSNetwork struct (ManagementIP, etc.)
	return hcsshim.GetHNSNetworkByName(info.Name)
}

// lookupNodeBGPIPs returns the IP addresses Calico has chosen for this
// node's BGP source — i.e. the result of IP_AUTODETECTION_METHOD /
// IP6_AUTODETECTION_METHOD applied at calico-node -startup. These are
// stored as CIDR strings on node.Spec.BGP, e.g. "10.2.0.3/24" or
// "fd5a:8000:1:0:1ac0:4dff:fe89:5194/64". We strip the prefix length
// because HNS expects bare IPs in ManagementIP/ManagementIPv6.
//
// Returns ("","") on any error or missing data so callers can fall back
// to legacy HNS auto-pick behaviour. NODENAME is read from the env (set
// by the calico-node-windows DaemonSet via the Downward API).
func lookupNodeBGPIPs(ctx context.Context, calicoClient calicoclient.Interface, logger *logrus.Entry) (string, string) {
	nodeName := os.Getenv("NODENAME")
	if nodeName == "" {
		nodeName = os.Getenv("HOSTNAME")
	}
	if nodeName == "" {
		logger.Warn("Cannot determine node name; HNS ManagementIP/v6 will be auto-picked")
		return "", ""
	}
	node, err := calicoClient.Nodes().Get(ctx, nodeName, options.GetOptions{})
	if err != nil {
		logger.WithError(err).Warnf("Failed to look up node %s; HNS ManagementIP/v6 will be auto-picked", nodeName)
		return "", ""
	}
	if node.Spec.BGP == nil {
		return "", ""
	}
	stripPrefix := func(s string) string {
		if i := strings.IndexByte(s, '/'); i >= 0 {
			return s[:i]
		}
		return s
	}
	return stripPrefix(node.Spec.BGP.IPv4Address), stripPrefix(node.Spec.BGP.IPv6Address)
}

func EnsureVXLANTunnelAddr(ctx context.Context, calicoClient calicoclient.Interface, nodeName string, ipNet *net.IPNet, networkName string) error {
	logrus.Debug("Checking the node's VXLAN tunnel address")
	var updateRequired bool
	node, err := calicoClient.Nodes().Get(ctx, nodeName, options.GetOptions{})
	if err != nil {
		return err
	}

	expectedIP := getNthIP(ipNet, 1).String()
	if node.Spec.IPv4VXLANTunnelAddr != expectedIP {
		logrus.WithField("ip", expectedIP).Debug("VXLAN tunnel IP to be updated")
		updateRequired = true
	}

	mac, err := GetDRMACAddr(networkName, ipNet)
	if err != nil {
		return err
	}
	expectedMAC := mac.String()
	if node.Spec.VXLANTunnelMACAddr != expectedMAC {
		logrus.WithField("mac", expectedMAC).Debug("VXLAN tunnel MAC to be updated")
		updateRequired = true
	}

	if updateRequired == false {
		return nil
	}

	node.Spec.IPv4VXLANTunnelAddr = expectedIP
	node.Spec.VXLANTunnelMACAddr = expectedMAC
	_, err = calicoClient.Nodes().Update(ctx, node, options.SetOptions{})
	return err
}

func createAndAttachVxlanHostEP(epName string, hnsNetwork *hcsshim.HNSNetwork, subNet *net.IPNet, logger *logrus.Entry) (*hcsshim.HNSEndpoint, error) {
	var err error
	endpointAddress := getNthIP(subNet, 2)

	// 1. Check if the HNSEndpoint exists and has the expected settings
	existingEndpoint, err := hcsshim.GetHNSEndpointByName(epName)
	if err == nil && existingEndpoint.VirtualNetwork == hnsNetwork.Id {
		// Check policies if there is PA type
		targetType := "PA"
		for _, policy := range existingEndpoint.Policies {
			policyType, _ := jsonparser.GetUnsafeString(policy, "Type")
			if policyType == targetType {
				actualPaIP, _ := jsonparser.GetUnsafeString(policy, targetType)
				if actualPaIP == hnsNetwork.ManagementIP {
					logger.Infof("Found existing remote HNSEndpoint %s", epName)
					return existingEndpoint, nil
				}
			}
		}
	}

	// 2. Create a new HNSNetwork
	if existingEndpoint != nil {
		if _, err := existingEndpoint.Delete(); err != nil {
			return nil, errors.Annotatef(err, "failed to delete existing remote HNSEndpoint %s", epName)
		}
		logger.Infof("Deleted stale HNSEndpoint %s", epName)
	}

	macAddr := GetMacAddr(hnsNetwork.ManagementIP) //nolint:all

	newEndpoint := &hcsshim.HNSEndpoint{
		Name:             epName,
		IPAddress:        endpointAddress,
		MacAddress:       macAddr,
		VirtualNetwork:   hnsNetwork.Id,
		IsRemoteEndpoint: true,
		Policies: []json.RawMessage{
			[]byte(fmt.Sprintf(`{"Type":"PA","PA":"%s"}`, hnsNetwork.ManagementIP)),
		},
	}
	if _, err := newEndpoint.Create(); err != nil {
		return nil, errors.Annotatef(err, "failed to create remote HNSEndpoint %s", epName)
	}
	logger.Infof("Created HNSEndpoint %s", epName)

	return newEndpoint, nil
}

// realHNSEndpoint bridges HNSEndpointAPI to hcsshim.
type realHNSEndpoint struct{}

func (r *realHNSEndpoint) GetByName(name string) (*HNSEndpointInfo, error) {
	ep, err := hcsshim.GetHNSEndpointByName(name)
	if err != nil {
		return nil, err
	}
	return hcsshimEndpointToInfo(ep), nil
}

func (r *realHNSEndpoint) Delete(endpoint *HNSEndpointInfo) error {
	ep, err := hcsshim.GetHNSEndpointByName(endpoint.Name)
	if err != nil {
		return err
	}
	_, err = ep.Delete()
	return err
}

func (r *realHNSEndpoint) Create(jsonRequest string) (*HNSEndpointInfo, error) {
	ep, err := hcsshim.HNSEndpointRequest("POST", "", jsonRequest)
	if err != nil {
		return nil, err
	}
	return hcsshimEndpointToInfo(ep), nil
}

func (r *realHNSEndpoint) HostAttach(endpoint *HNSEndpointInfo, compartmentID uint16) error {
	ep, err := hcsshim.GetHNSEndpointByName(endpoint.Name)
	if err != nil {
		return err
	}
	return ep.HostAttach(compartmentID)
}

var defaultHNSEndpoint HNSEndpointAPI = &realHNSEndpoint{}

func hcsshimEndpointToInfo(ep *hcsshim.HNSEndpoint) *HNSEndpointInfo {
	return &HNSEndpointInfo{
		Id:             ep.Id,
		Name:           ep.Name,
		VirtualNetwork: ep.VirtualNetwork,
		IPAddress:      ep.IPAddress,
	}
}

// CreateAndAttachHostEP creates (or reuses) the host endpoint on an HNS network.
// createAndAttachHostEPWithAPI (cross-platform) lives in hns_types.go.
func CreateAndAttachHostEP(epName string, hnsNetwork *hcsshim.HNSNetwork, subNet *net.IPNet, logger *logrus.Entry) (*hcsshim.HNSEndpoint, error) {
	netInfo := hcsshimNetworkToInfo(hnsNetwork)
	info, err := createAndAttachHostEPWithAPI(epName, netInfo, subNet, logger, defaultHNSEndpoint)
	if err != nil {
		return nil, err
	}
	// Re-fetch from hcsshim to get the full HNSEndpoint struct.
	return hcsshim.GetHNSEndpointByName(info.Name)
}

func chkMgmtIPandEnableForwarding(networkName string, hnsEndpoint *hcsshim.HNSEndpoint, logger *logrus.Entry) (network *hcsshim.HNSNetwork, err error) {
	startTime := time.Now()
	logCxt := logger.WithField("network", networkName)

	// Wait for the network to populate Management IP and for it to match one of the host interfaces.
	for {
		// Look up the network afresh each time, in case the management IP changes.
		network, err = hcsshim.GetHNSNetworkByName(networkName)
		if err != nil {
			logger.Errorf("Unable to get hns network %s after creation, error: %v", networkName, err)
			return nil, err
		}

		if time.Since(startTime) > 30*time.Second {
			return nil, fmt.Errorf(
				"timed out waiting for interface matching the management IP (%v) of network %s",
				network.ManagementIP, networkName)
		}

		if len(network.ManagementIP) == 0 {
			logCxt.Info("Waiting for management IP...")
			time.Sleep(1 * time.Second)
			continue
		}

		mgmtIP := net.ParseIP(network.ManagementIP)
		if mgmtIface, err := lookupManagementIface(mgmtIP, logger); err != nil {
			logCxt.WithField("ip", network.ManagementIP).WithError(err).Warn(
				"Waiting for interface matching management IP...")
			time.Sleep(1 * time.Second)
			continue
		} else {
			err := enableForwarding(mgmtIface, logger)
			if err != nil {
				return nil, err
			}
		}

		break
	}

	ourEpAddr := hnsEndpoint.IPAddress.String()
	netInterface, err := lookupManagementIface(net.ParseIP(ourEpAddr), logger)
	if err != nil {
		logger.WithError(err).Errorf("Unable to find interface matching our host endpoint [%v]", ourEpAddr)
		return nil, err
	}

	logger.Infof("Found Interface with IP[%s]: %v", ourEpAddr, netInterface)
	err = enableForwarding(netInterface, logger)
	if err != nil {
		return nil, err
	}

	return network, nil
}

func enableForwarding(netInterface net.Interface, logger *logrus.Entry) error {
	interfaceIdx := strconv.Itoa(netInterface.Index)
	cmd := fmt.Sprintf("Set-NetIPInterface -ifIndex %s -AddressFamily IPv4 -Forwarding Enabled", interfaceIdx)
	if _, _, err := winutils.Powershell(cmd); err != nil {
		logger.WithError(err).Errorf("Unable to enable IPv4 forwarding on [%v] index [%v]",
			netInterface.Name, interfaceIdx)
		return err
	}
	logger.Infof("Enabled IPv4 forwarding on [%v] index [%v]", netInterface.Name, interfaceIdx)

	// Also enable IPv6 forwarding for dual-stack support.
	cmdV6 := fmt.Sprintf("Set-NetIPInterface -ifIndex %s -AddressFamily IPv6 -Forwarding Enabled", interfaceIdx)
	if _, _, err := winutils.Powershell(cmdV6); err != nil {
		// IPv6 forwarding is best-effort; the interface may not have IPv6.
		logger.WithError(err).Warnf("Unable to enable IPv6 forwarding on [%v] index [%v]",
			netInterface.Name, interfaceIdx)
	} else {
		logger.Infof("Enabled IPv6 forwarding on [%v] index [%v]", netInterface.Name, interfaceIdx)
	}

	return nil
}

func (d *windowsDataplane) createAndAttachContainerEP(args *skel.CmdArgs,
	hnsNetwork *hcsshim.HNSNetwork,
	affineBlockSubnet *net.IPNet,
	allIPAMPools []*net.IPNet,
	natOutgoing bool,
	result *cniv1.Result,
	n *hns.NetConf,
	podIPv6 net.IP,
	subNetV6 *net.IPNet) (*hcsshim.HNSEndpoint, *hcn.HostComputeEndpoint, error) {

	var gatewayAddress string
	if d.conf.Mode == "vxlan" {
		gatewayAddress = getNthIP(affineBlockSubnet, 1).String()
	} else {
		gatewayAddress = getNthIP(affineBlockSubnet, 2).String()
	}

	natExclusions := allIPAMPools

	mgmtIP := net.ParseIP(hnsNetwork.ManagementIP)
	if len(mgmtIP) == 0 {
		// We just checked the management IP so we shouldn't lose it again.
		return nil, nil, fmt.Errorf("HNS network lost its management IP")
	}

	v1pols, v2pols, err := winpol.CalculateEndpointPolicies(n, natExclusions, natOutgoing, mgmtIP, d.logger)
	if err != nil {
		return nil, nil, err
	}

	endpointName := hns.ConstructEndpointName(args.ContainerID, args.Netns, n.Name)
	epIP := result.IPs[0].Address.IP
	epIPBytes := epIP.To4()
	macAddr := ""

	if d.conf.Mode == "vxlan" {
		vxlanMACPrefix := d.conf.VXLANMacPrefix
		if len(vxlanMACPrefix) != 0 {
			if len(vxlanMACPrefix) != 5 || vxlanMACPrefix[2] != '-' {
				return nil, nil, fmt.Errorf("endpointMacPrefix [%v] is invalid, value must be of the format xx-xx", vxlanMACPrefix)
			}
		} else {
			vxlanMACPrefix = "0E-2A"
		}
		// conjure a MAC based on the IP for Overlay
		macAddr = fmt.Sprintf("%v-%02x-%02x-%02x-%02x", vxlanMACPrefix, epIPBytes[0], epIPBytes[1], epIPBytes[2], epIPBytes[3])

		v1pols = append(v1pols, []json.RawMessage{
			[]byte(fmt.Sprintf(`{"Type":"PA","PA":"%s"}`, hnsNetwork.ManagementIP)),
		}...)

		hcnPol := hcn.EndpointPolicy{
			Type: hcn.NetworkProviderAddress,
			Settings: json.RawMessage(
				fmt.Sprintf(`{"ProviderAddress":"%s"}`, hnsNetwork.ManagementIP),
			),
		}
		v2pols = append(v2pols, hcnPol)
	} else {
		// Add an entry to force encap to the management IP.  We think this is required for node ports. The encap is
		// local to the host so there's no real vxlan going on here.
		dict := map[string]interface{}{
			"Type":              "ROUTE",
			"DestinationPrefix": mgmtIP.String() + "/32",
			"NeedEncap":         true,
		}
		encoded, err := json.Marshal(dict)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to add route encap policy")
		}

		v1pols = append(v1pols, json.RawMessage(encoded))

		hcnPol := hcn.EndpointPolicy{
			Type: hcn.SDNRoute,
			Settings: json.RawMessage(
				fmt.Sprintf(`{"DestinationPrefix": "%s", "NeedEncap": true}`, mgmtIP.String()+"/32"),
			),
		}
		v2pols = append(v2pols, hcnPol)
	}

	// if supported add loopback DSR
	if d.conf.WindowsLoopbackDSR {
		// v1
		v1pols = append(v1pols, []json.RawMessage{
			[]byte(fmt.Sprintf(`{"Type":"OutBoundNAT","Destinations":["%s"]}`, epIP.String())),
		}...)

		// v2
		loopBackPol := hcn.EndpointPolicy{
			Type: hcn.OutBoundNAT,
			Settings: json.RawMessage(
				fmt.Sprintf(`{"Destinations":["%s"]}`, epIP.String()),
			),
		}
		v2pols = append(v2pols, loopBackPol)
	} else {
		d.logger.Info("DSR not supported")
	}

	isDockerV1 := cri.IsDockershimV1(args.Netns)
	attempts := 3
	for {
		var hnsEndpointCont *hcsshim.HNSEndpoint
		var hcsEndpoint *hcn.HostComputeEndpoint
		var err error

		// Create the container endpoint. For Dockershim, use the V1 API.
		// For remote runtimes, we use the V2 API.
		if isDockerV1 {
			d.logger.Infof("Attempting to create HNS endpoint name: %s for container", endpointName)
			_, err = hns.AddHnsEndpoint(endpointName, hnsNetwork.Id, args.ContainerID, args.Netns, func() (*hcsshim.HNSEndpoint, error) {
				hnsEP := &hcsshim.HNSEndpoint{
					Name:           endpointName,
					VirtualNetwork: hnsNetwork.Id,
					DNSServerList:  strings.Join(result.DNS.Nameservers, ","),
					DNSSuffix:      strings.Join(result.DNS.Search, ","),
					GatewayAddress: gatewayAddress,
					IPAddress:      epIP,
					MacAddress:     macAddr,
					Policies:       v1pols,
				}
				return hnsEP, nil
			})

			// We cannot trust hns.ProvisionEndpoint error status. https://github.com/containernetworking/plugins/blob/v0.8.6/pkg/hns/endpoint_windows.go#L244
			// For instance, if a container exited for any reason when we reach here,
			// hns.ProvisionEndpoint will follow the execution steps below:
			// 1. Create endpoint
			// 2. Failed to attach endpoint because of error "The requested virtual machine or container operation is not valid in the current state."
			//    and return hcsshim.ErrComputeSystemDoesNotExist
			// 3. Deprovision endpoint
			// 4. Return endpoint with no error. However, endpoint is no longer in the system.

			// However, both upstream win_bridge and win_overlay plugins do not handle this case.
			if err == nil {
				// Evaluate endpoint status by reading from the system.
				hnsEndpointCont, err = hcsshim.GetHNSEndpointByName(endpointName)

				d.logger.Infof("Endpoint to container created! %v", hnsEndpointCont)
			}
		} else {
			d.logger.Infof("Attempting to create HostComputeEndpoint: %s for container", endpointName)

			// Build dual-stack IP configs and routes for the HCN endpoint.
			ipConfigs := []hcn.IpConfig{{IpAddress: epIP.String()}}
			hcnRoutes := []hcn.Route{{NextHop: gatewayAddress, DestinationPrefix: "0.0.0.0/0"}}
			if podIPv6 != nil && subNetV6 != nil {
				ipConfigs = append(ipConfigs, hcn.IpConfig{IpAddress: podIPv6.String()})
				gwV6 := getNthIP(subNetV6, 2)
				if d.conf.Mode == "vxlan" {
					gwV6 = getNthIP(subNetV6, 1)
				}
				hcnRoutes = append(hcnRoutes, hcn.Route{NextHop: gwV6.String(), DestinationPrefix: "::/0"})
			}

			hcsEndpoint, err = hns.AddHcnEndpoint(endpointName, hnsNetwork.Id, args.Netns, func() (*hcn.HostComputeEndpoint, error) {
				hce := &hcn.HostComputeEndpoint{
					Name:               endpointName,
					HostComputeNetwork: hnsNetwork.Id,
					Dns: hcn.Dns{
						Domain:     result.DNS.Domain,
						Search:     result.DNS.Search,
						ServerList: result.DNS.Nameservers,
						Options:    result.DNS.Options,
					},
					MacAddress:       macAddr,
					Routes:           hcnRoutes,
					IpConfigurations: ipConfigs,
					SchemaVersion: hcn.SchemaVersion{
						Major: 2,
					},
					Policies: v2pols,
				}
				return hce, nil
			})

			if err == nil {
				d.logger.Infof("Endpoint to container created! %v", hcsEndpoint)
			}
		}

		if err != nil {
			d.logger.WithError(err).Error("Error provisioning endpoint, checking if we need to clean it up.")

			// If a previous call failed here, before cleaning up, it may have left an orphaned endpoint.  Check for that
			// and clean up.
			cleanupErr := cleanUpEndpointByIP(epIP, d.logger, isDockerV1)
			if cleanupErr != nil {
				d.logger.WithError(err).Error("Failed to clean up (by IP) after failure.")
			} else {
				d.logger.Info("Cleanup (by IP) succeeded.")
			}

			// If provision endpoint fails at the attach stage, we can be left with an orphaned endpoint.  Check for
			// that and clean it up.
			cleanupErr = cleanUpEndpointByName(endpointName, d.logger, isDockerV1)
			if cleanupErr != nil {
				d.logger.WithError(err).Error("Failed to clean up (by name) after failure.")
			} else {
				d.logger.Info("Cleanup (by name) succeeded.")
			}

			if attempts > 0 {
				// Cleanup may have unblocked another attempt, see if we can retry...
				d.logger.Info("Retrying...")
				attempts--
				time.Sleep(time.Second)
				continue
			}
			return nil, nil, err
		}

		return hnsEndpointCont, hcsEndpoint, nil
	}
}

func cleanUpEndpointByIP(IP net.IP, logger *logrus.Entry, isDockerV1 bool) error {
	if isDockerV1 {
		endpoints, err := hcsshim.HNSListEndpointRequest()
		if err != nil {
			logger.WithError(err).Error("Failed to list endpoints")
			return err
		}
		for _, ep := range endpoints {
			if ep.IPAddress.Equal(IP) {
				logger.WithField("conflictingEndpoint", ep).Error("Found preexisting conflicting endpoint.")
				_, err := ep.Delete()
				if err != nil {
					logger.WithError(err).Error("Failed to delete old endpoint")
				}
				return err // Exit early since there can be only one endpoint with the same IP.
			}
		}
	} else {
		endpoints, err := hcn.ListEndpoints()
		if err != nil {
			logger.WithError(err).Error("Failed to list endpoints")
			return err
		}
		for _, ep := range endpoints {
			for _, ipConf := range ep.IpConfigurations {
				if ipConf.IpAddress == IP.String() {
					logger.WithField("conflictingEndpoint", ep).Error("Found preexisting conflicting host compute endpoint.")
					err := ep.Delete()
					if err != nil {
						logger.WithError(err).Error("Failed to delete old host compute endpoint")
					}
					return err // Exit early since there can be only one endpoint with the same IP.
				}
			}
		}
	}
	return nil
}

func cleanUpEndpointByName(endpointName string, logger *logrus.Entry, isDockerV1 bool) error {
	if isDockerV1 {
		hnsEndpoint, err := hcsshim.GetHNSEndpointByName(endpointName)
		if hcsshim.IsNotExist(err) {
			logger.Debug("Endpoint already gone.  Nothing to do.")
			return nil
		}
		if err != nil {
			logger.WithError(err).Error("Failed to get endpoint for cleanup.")
			return err
		}

		_, err = hnsEndpoint.Delete()
		return err
	} else {
		hceEndpoint, err := hcn.GetEndpointByName(endpointName)
		if hcn.IsNotFoundError(err) {
			logger.Debug("Endpoint already gone.  Nothing to do.")
			return nil
		}
		if err != nil {
			logger.WithError(err).Error("Failed to get endpoint for cleanup.")
			return err
		}

		err = hceEndpoint.Delete()
		return err
	}
}

func lookupManagementIface(mgmtIP net.IP, logger *logrus.Entry) (net.Interface, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		logger.WithError(err).Error("Failed to look up host interfaces")
		return net.Interface{}, err
	}

	for _, iface := range interfaces {
		addrs, err := iface.Addrs()
		if err != nil {
			logger.WithError(err).WithField("iface", iface.Name).Error(
				"Failed to look up host interface addresses")
			return net.Interface{}, err
		}
		for _, addr := range addrs {
			if ipAddr, ok := addr.(*net.IPNet); ok {
				if ipAddr.Contains(mgmtIP) {
					return iface, nil
				}
			}
		}
	}
	return net.Interface{}, fmt.Errorf("couldn't find an interface matching management IP %s", mgmtIP.String())
}

func lookupManagementAddr(mgmtIP net.IP, logger *logrus.Entry) (*net.IPNet, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		logger.WithError(err).Error("Failed to look up host interfaces")
		return nil, err
	}

	for _, iface := range interfaces {
		addrs, err := iface.Addrs()
		if err != nil {
			logger.WithError(err).WithField("iface", iface.Name).Error(
				"Failed to look up host interface addresses")
			return nil, err
		}
		for _, addr := range addrs {
			if ipAddr, ok := addr.(*net.IPNet); ok {
				if ipAddr.Contains(mgmtIP) {
					return ipAddr, nil
				}
			}
		}
	}
	return nil, fmt.Errorf("couldn't find an interface matching management IP %s", mgmtIP.String())
}

// getNthIP increments the subnet IP address by n depending on
// endpoint IP or gateway IP. Supports both IPv4 and IPv6.
// getNthIP and CreateNetworkName are defined in hns_types.go (cross-platform).

// SetupRoutes sets up the routes for the host side of the veth pair.
func SetupRoutes(hostVeth interface{}, result *cniv1.Result) error {

	// Go through all the IPs and add routes for each IP in the result.
	for _, ipAddr := range result.IPs {
		logrus.WithFields(logrus.Fields{"interface": hostVeth, "IP": ipAddr.Address}).Debugf("STUB: CNI adding route")
	}
	return nil
}

// CleanUpNamespace deletes the devices in the network namespace.
func (d *windowsDataplane) CleanUpNamespace(args *skel.CmdArgs) error {
	d.logger.Infof("Cleaning up endpoint")

	n, _, err := loadNetConf(args.StdinData)
	if err != nil {
		return err
	}

	epName := hns.ConstructEndpointName(args.ContainerID, args.Netns, n.Name)
	d.logger.Infof("Attempting to delete HNS endpoint name : %s for container", epName)

	if cri.IsDockershimV1(args.Netns) {
		err = hns.RemoveHnsEndpoint(epName, args.Netns, args.ContainerID)
		if err != nil && strings.Contains(err.Error(), "not found") {
			d.logger.WithError(err).Warn("Endpoint not found during delete, assuming it's already been cleaned up")
			return nil
		}
	} else {
		// RemoveHcnEndpoint returns nil if error was because the ep doesn't
		// exist.
		err = hns.RemoveHcnEndpoint(epName)
		if err != nil {
			d.logger.WithError(err).Warn("Failed to find or delete endpoint, assuming it's already been cleaned up")
			return nil
		}
	}
	return err
}

// NetworkApplicationContainer tries to attach the application container to the endpoint that is attached to its pause container.
// On failure, it returns the error.
// This is done so that the DNS details are reflected in the container.
func NetworkApplicationContainer(args *skel.CmdArgs) error {
	n, _, err := loadNetConf(args.StdinData)
	hnsEndpointName := hns.ConstructEndpointName(args.ContainerID, args.Netns, n.Name)

	hnsEndpoint, err := hcsshim.GetHNSEndpointByName(hnsEndpointName)
	if err != nil {
		logrus.Errorf("Endpoint does not exist with hns endpoint name: %v\n ", hnsEndpointName)
		return err
	}

	if err = hcsshim.HotAttachEndpoint(args.ContainerID, hnsEndpoint.Id); err != nil {
		if err == hcsshim.ErrComputeSystemDoesNotExist {
			// kubelet Windows uses ADD CmdArgs to get pod status. It is possible for Calico CNI to receive an ADD after application container has completed and been removed from runtime.
			// In that case, return nil to allow Calico CNI to return good pod status to kubelet.
			return nil
		}
		logrus.Errorf("Failed to attach hns endpoint: %v to container: %v\n ", hnsEndpoint, args.ContainerID)
		return err
	}

	return nil
}

// GetMacAddr gets the MAC hardware
// address of the host machine
func GetMacAddr(mgmtIp string) (addr string) {
	interfaces, err := net.Interfaces()
	if err == nil {
	outerLoop:
		for _, i := range interfaces {
			addrs, err := i.Addrs()
			if err == nil {
				for _, j := range addrs {
					ip := strings.Split(j.String(), "/")
					if strings.Compare(ip[0], mgmtIp) == 0 {
						addr = i.HardwareAddr.String()
						break outerLoop
					}
				}
			}
		}
	}
	return
}

func GetDRMACAddr(networkName string, subNet *net.IPNet) (net.HardwareAddr, error) {
	hnsNetwork, err := hcsshim.GetHNSNetworkByName(networkName)
	if err != nil {
		logrus.Infof("hns network %s not found", networkName)
		return nil, err
	}

	hcnNetwork, err := hcn.GetNetworkByName(networkName)
	if err != nil {
		logrus.Infof("hcn network %s not found", networkName)
		return nil, err
	}

	err = hcn.RemoteSubnetSupported()
	if err != nil {
		logrus.Infof("remote subnet not supported")
		return nil, err
	}

	var remoteDRMAC string
	var providerAddress string
	logrus.Infof("Checking HNS network for DR MAC : [%+v]", hnsNetwork)
	for _, policy := range hcnNetwork.Policies {
		logrus.Infof("inside for loop. policy = [%+v]", policy)
		if policy.Type == hcn.DrMacAddress {
			logrus.Infof("policy type is drmacaddress")
			policySettings := hcn.DrMacAddressNetworkPolicySetting{}
			err = json.Unmarshal(policy.Settings, &policySettings)
			if err != nil {
				return nil, fmt.Errorf("Failed to unmarshal settings")
			}
			remoteDRMAC = policySettings.Address
			logrus.Infof("remote dr mac = %v", remoteDRMAC)
		}
		if policy.Type == hcn.ProviderAddress {
			logrus.Infof("policy type is provideraddress")
			policySettings := hcn.ProviderAddressEndpointPolicySetting{}
			err = json.Unmarshal(policy.Settings, &policySettings)
			if err != nil {
				return nil, fmt.Errorf("Failed to unmarshal settings")
			}
			providerAddress = policySettings.ProviderAddress
			logrus.Infof("providerAddress = %v", providerAddress)
		}
	}
	if providerAddress != hnsNetwork.ManagementIP {
		logrus.Infof("Cannot use DR MAC %v since PA %v does not match %v", remoteDRMAC, providerAddress, hnsNetwork.ManagementIP)
		remoteDRMAC = ""
	}

	if len(providerAddress) == 0 {
		return nil, fmt.Errorf("Cannot find network with Management IP %v", hnsNetwork.ManagementIP)
	}
	if len(remoteDRMAC) == 0 {
		return nil, fmt.Errorf("Could not find remote DR MAC for Management IP %v", hnsNetwork.ManagementIP)
	}
	mac, err := net.ParseMAC(string(remoteDRMAC))
	if err != nil {
		return nil, fmt.Errorf("Cannot parse DR MAC %v: %+v", remoteDRMAC, err)
	}

	logrus.Infof("mac address = %v", mac)
	return mac, nil
}
