// Copyright (c) 2019 Tigera, Inc. All rights reserved.
package windataplane

import (
	"strings"
	"testing"

	. "github.com/onsi/gomega"

	"github.com/projectcalico/calico/felix/dataplane/windows/hns"
	"github.com/projectcalico/calico/felix/dataplane/windows/policysets"
	"github.com/projectcalico/calico/felix/proto"
)

func TestFlatten(t *testing.T) {
	RegisterTestingT(t)

	t.Log("Should have no effect on a single tier with no pass rules.")
	tier1 := []*hns.ACLPolicy{
		{Protocol: 256, Action: hns.Block},
		{Protocol: 256, Action: hns.Block},
	}
	flatSingleTier := flattenTiers([][]*hns.ACLPolicy{tier1})
	Expect(flatSingleTier).To(Equal(tier1))

	t.Log("Should discard unreachable tiers")
	tiers := [][]*hns.ACLPolicy{
		tier1,
		tier1,
	}
	Expect(flattenTiers(tiers)).To(Equal(tier1))

	t.Log("Should expand a pass rule")
	tierWithPass := []*hns.ACLPolicy{
		{Protocol: 256, Action: policysets.ActionPass},
		{Protocol: 100, Action: hns.Block},
		{Protocol: 100, Action: hns.Block},
	}
	Expect(flattenTiers([][]*hns.ACLPolicy{
		tierWithPass,
		tier1,
	})).To(Equal([]*hns.ACLPolicy{
		// tier1
		{Protocol: 256, Action: hns.Block},
		{Protocol: 256, Action: hns.Block},
		// Remainder of tierWithPass
		{Protocol: 100, Action: hns.Block},
		{Protocol: 100, Action: hns.Block},
	}))

	t.Log("Should combine protocol=any with a specific protocol")
	Expect(flattenTiers([][]*hns.ACLPolicy{
		{{Protocol: 256, Action: policysets.ActionPass}},
		{{Protocol: 10, Action: hns.Allow}},
	})).To(Equal([]*hns.ACLPolicy{
		{Protocol: 10, Action: hns.Allow},
	}))

	t.Log("Should combine CIDRs")
	Expect(flattenTiers([][]*hns.ACLPolicy{
		{
			{Action: policysets.ActionPass, LocalAddresses: "10.0.0.0/16"},
			{Action: policysets.ActionPass, LocalAddresses: "10.0.10.0/26"},
		},
		{{Action: hns.Allow, LocalAddresses: "10.0.10.0/24"}},
	})).To(Equal([]*hns.ACLPolicy{
		{Action: hns.Allow, LocalAddresses: "10.0.10.0/24"},
		{Action: hns.Allow, LocalAddresses: "10.0.10.0/26"},
	}))
	Expect(flattenTiers([][]*hns.ACLPolicy{
		{
			{Action: policysets.ActionPass, RemoteAddresses: "10.0.0.0/16,11.0.0.0/24"},
			{Action: policysets.ActionPass, RemoteAddresses: "10.0.10.0/26"},
		},
		{{Action: hns.Allow, RemoteAddresses: "10.0.10.0/24,11.0.0.0/8"},
			{Action: hns.Allow, RemoteAddresses: "12.0.0.0/8"},
			{Action: hns.Allow, LocalAddresses: "12.0.0.0/8"},
		},
	})).To(Equal([]*hns.ACLPolicy{
		// First pass rule
		{Action: hns.Allow, RemoteAddresses: "10.0.10.0/24,11.0.0.0/24" /*remotes get intersected*/},
		{Action: hns.Allow,
			RemoteAddresses: "10.0.0.0/16,11.0.0.0/24", /*second rule from second tier has no remotes, so inherits from pass rule*/
			LocalAddresses:  "12.0.0.0/8"},
		// Second pass rule
		{Action: hns.Allow, RemoteAddresses: "10.0.10.0/26"},
		{Action: hns.Allow, RemoteAddresses: "10.0.10.0/26", LocalAddresses: "12.0.0.0/8"},
	}))

	t.Log("Should combine Ports")
	Expect(flattenTiers([][]*hns.ACLPolicy{
		{
			{Action: policysets.ActionPass, LocalPorts: "1,2,10-15"},
			{Action: policysets.ActionPass, LocalPorts: "10-15"},
		},
		{{Action: hns.Allow, LocalPorts: "2,12-16,55"}},
	})).To(Equal([]*hns.ACLPolicy{
		{Action: hns.Allow, LocalPorts: "2,12-15"},
		{Action: hns.Allow, LocalPorts: "12-15"},
	}))
	Expect(flattenTiers([][]*hns.ACLPolicy{
		{
			{Action: policysets.ActionPass, RemotePorts: "1,2,10-15"},
			{Action: policysets.ActionPass, RemotePorts: "10-15"},
		},
		{{Action: hns.Allow, RemotePorts: "2,12-16,55"}},
	})).To(Equal([]*hns.ACLPolicy{
		{Action: hns.Allow, RemotePorts: "2,12-15"},
		{Action: hns.Allow, RemotePorts: "12-15"},
	}))

	t.Log("Should recurse with non-overlapping pass on second tier")
	Expect(flattenTiers([][]*hns.ACLPolicy{
		{
			{Action: policysets.ActionPass, RemoteAddresses: "10.0.0.0/16,11.0.0.0/24"},
			{Action: policysets.ActionPass, RemoteAddresses: "10.0.10.0/26"},
		},
		{{Action: hns.Allow, RemoteAddresses: "10.0.10.0/24,11.0.0.0/8"},
			{Action: policysets.ActionPass, RemoteAddresses: "12.0.0.0/8"},
			{Action: hns.Allow, LocalAddresses: "12.0.0.0/8"},
		},
		{
			{Action: hns.Allow, RemoteAddresses: "10.0.11.0/28"},
			{Action: hns.Block, RemoteAddresses: "10.0.10.0/28"},
		},
	})).To(Equal([]*hns.ACLPolicy{
		// First pass rule
		{Action: hns.Allow, RemoteAddresses: "10.0.10.0/24,11.0.0.0/24"},
		{Action: hns.Allow, RemoteAddresses: "10.0.0.0/16,11.0.0.0/24", LocalAddresses: "12.0.0.0/8"},
		// Second pass rule
		{Action: hns.Allow, RemoteAddresses: "10.0.10.0/26"},
		{Action: hns.Allow, RemoteAddresses: "10.0.10.0/26", LocalAddresses: "12.0.0.0/8"},
	}))

	t.Log("Should recurse with overlapping pass on second tier")
	Expect(flattenTiers([][]*hns.ACLPolicy{
		{
			{Action: policysets.ActionPass, RemoteAddresses: "10.0.0.0/16,11.0.0.0/24"},
			{Action: policysets.ActionPass, RemoteAddresses: "10.0.10.0/26"},
		},
		{{Action: hns.Allow, RemoteAddresses: "10.0.10.0/24,11.0.0.0/8"},
			{Action: policysets.ActionPass, RemoteAddresses: "10.0.10.0/24"},
			{Action: hns.Allow, LocalAddresses: "12.0.0.0/8"},
		},
		{
			{Action: hns.Allow, RemoteAddresses: "10.0.11.0/28"},
			{Action: hns.Block, RemoteAddresses: "10.0.10.0/28"},
		},
	})).To(Equal([]*hns.ACLPolicy{
		// First pass rule
		{Action: hns.Allow, RemoteAddresses: "10.0.10.0/24,11.0.0.0/24"},
		{Action: hns.Block, RemoteAddresses: "10.0.10.0/28"},
		{Action: hns.Allow, RemoteAddresses: "10.0.0.0/16,11.0.0.0/24", LocalAddresses: "12.0.0.0/8"},
		// Second pass rule
		{Action: hns.Allow, RemoteAddresses: "10.0.10.0/26"},
		{Action: hns.Block, RemoteAddresses: "10.0.10.0/28"},
		{Action: hns.Allow, RemoteAddresses: "10.0.10.0/26", LocalAddresses: "12.0.0.0/8"},
	}))

	t.Log("Should block with pass in last tier")
	Expect(flattenTiers([][]*hns.ACLPolicy{
		{
			{Action: hns.Allow, RemoteAddresses: "10.0.10.0/24", LocalPorts: "6000"},
			{Action: policysets.ActionPass, RemoteAddresses: "10.0.10.0/24"},
		},
		{
			{Action: hns.Block, RemoteAddresses: "10.0.10.1/32", LocalPorts: "6379"},
			{Action: policysets.ActionPass, RemoteAddresses: "10.0.10.2/32", LocalPorts: "6380, 6381"},
			{Action: hns.Block, RemoteAddresses: "10.0.0.0/8", LocalPorts: "6390-6400"},
		},
	})).To(Equal([]*hns.ACLPolicy{
		{Action: hns.Allow, RemoteAddresses: "10.0.10.0/24", LocalPorts: "6000"},
		{Action: hns.Block, RemoteAddresses: "10.0.10.1/32", LocalPorts: "6379"},
		{Action: hns.Block, RemoteAddresses: "10.0.10.2/32", LocalPorts: "6380, 6381"},
		{Action: hns.Block, RemoteAddresses: "10.0.10.0/24", LocalPorts: "6390-6400"},
	}))

	t.Log("Should pass to last tier which has only the rule from the profile")
	Expect(flattenTiers([][]*hns.ACLPolicy{
		{
			{Action: hns.Block, RemoteAddresses: "192.168.1.123/32", LocalPorts: "8080"},
			{Action: policysets.ActionPass},
		},
		{
			// This would be the allow rule added for the profile.
			{Action: hns.Allow, Protocol: 256},
		},
	})).To(Equal([]*hns.ACLPolicy{
		{Action: hns.Block, RemoteAddresses: "192.168.1.123/32", LocalPorts: "8080"},
		{Action: hns.Allow},
	}))

	t.Log("Should split mixed-family addresses inherited through a pass rule")
	Expect(flattenTiers([][]*hns.ACLPolicy{
		{
			{
				Action:          policysets.ActionPass,
				Protocol:        256,
				RemoteAddresses: "10.3.0.0/16,10.152.184.0/24,fc00::/7,fe80::/10",
			},
		},
		{
			{
				Id:             "allow-signaling",
				Action:         hns.Allow,
				Protocol:       6,
				LocalAddresses: "10.3.48.244/32,2001:5a8:4298:3b01:430d:9038:5fa1:d034/128",
				RemotePorts:    "443",
			},
		},
	})).To(Equal([]*hns.ACLPolicy{
		{
			Id:              "allow-signaling-af4",
			Action:          hns.Allow,
			Protocol:        6,
			LocalAddresses:  "10.3.48.244/32",
			RemoteAddresses: "10.3.0.0/16,10.152.184.0/24",
			RemotePorts:     "443",
		},
		{
			Id:              "allow-signaling-af6",
			Action:          hns.Allow,
			Protocol:        6,
			LocalAddresses:  "2001:5a8:4298:3b01:430d:9038:5fa1:d034/128",
			RemoteAddresses: "fc00::/7,fe80::/10",
			RemotePorts:     "443",
		},
	}))
}

func TestReWritePriority(t *testing.T) {
	RegisterTestingT(t)

	t.Log("Should write incrementing priority")
	policies := []*hns.ACLPolicy{
		{Action: hns.Allow, RemoteAddresses: "10.0.10.0/24", Priority: 1000},
		{Action: hns.Allow, RemoteAddresses: "10.0.11.0/24", Priority: 1001},
		{Action: hns.Block, RemoteAddresses: "10.0.12.0/24", Priority: 1002},
		{Action: hns.Block, RemoteAddresses: "10.0.13.0/24", Priority: 1003},
	}

	rewritePriorities(policies, policysets.PolicyRuleMaxPriority)
	Expect(policies).To(Equal([]*hns.ACLPolicy{
		{Action: hns.Allow, RemoteAddresses: "10.0.10.0/24", Priority: 1000},
		{Action: hns.Allow, RemoteAddresses: "10.0.11.0/24", Priority: 1001},
		{Action: hns.Block, RemoteAddresses: "10.0.12.0/24", Priority: 1002},
		{Action: hns.Block, RemoteAddresses: "10.0.13.0/24", Priority: 1003},
	}))

	t.Log("Should write aggregated priority")
	policies = []*hns.ACLPolicy{
		{Action: hns.Allow, RemoteAddresses: "10.0.10.0/24", Priority: 1000},
		{Action: hns.Allow, RemoteAddresses: "10.0.11.0/24", Priority: 1001},
		{Action: hns.Block, RemoteAddresses: "10.0.12.0/24", Priority: 1002},
		{Action: hns.Block, RemoteAddresses: "10.0.13.0/24", Priority: 1003},
	}

	rewritePriorities(policies, 1004)
	Expect(policies).To(Equal([]*hns.ACLPolicy{
		{Action: hns.Allow, RemoteAddresses: "10.0.10.0/24", Priority: 1000},
		{Action: hns.Allow, RemoteAddresses: "10.0.11.0/24", Priority: 1000},
		{Action: hns.Block, RemoteAddresses: "10.0.12.0/24", Priority: 1001},
		{Action: hns.Block, RemoteAddresses: "10.0.13.0/24", Priority: 1001},
	}))
}

func TestAppManaLivePolicyTierSplitsMixedFamilyRules(t *testing.T) {
	RegisterTestingT(t)

	tcp := &proto.Protocol{NumberOrName: &proto.Protocol_Name{Name: "TCP"}}
	udp := &proto.Protocol{NumberOrName: &proto.Protocol_Name{Name: "UDP"}}
	icmp := &proto.Protocol{NumberOrName: &proto.Protocol_Name{Name: "ICMP"}}
	icmpv6 := &proto.Protocol{NumberOrName: &proto.Protocol_Name{Name: "ICMPv6"}}

	h := mockHNS{}
	h.SupportedFeatures.Acl.AclRuleId = true
	h.SupportedFeatures.Acl.AclNoHostRulePriority = true
	ipsc := mockIPSetCache{IPSets: map[string][]string{}}
	ps := policysets.NewPolicySets(&h, []policysets.IPSetCache{&ipsc}, mockReader(""))

	ps.AddOrReplacePolicySet("policy-appmana-unity-runtime-egress", &proto.Policy{
		OutboundRules: []*proto.Rule{
			{Action: "allow", Protocol: udp, DstNet: []string{"10.152.184.10/32"}, DstPorts: []*proto.PortRange{{First: 53, Last: 53}}, RuleId: "dns-udp"},
			{Action: "allow", Protocol: tcp, DstNet: []string{"10.152.184.10/32"}, DstPorts: []*proto.PortRange{{First: 53, Last: 53}}, RuleId: "dns-tcp"},
			{Action: "allow", Protocol: tcp, DstNet: []string{"10.152.184.30/32"}, DstPorts: []*proto.PortRange{{First: 5555, Last: 5555}}, RuleId: "signaling"},
			{Action: "deny", IpVersion: 4, DstNet: []string{"10.3.0.0/16", "10.152.184.0/24", "0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.168.0.0/16", "224.0.0.0/4", "240.0.0.0/4"}, RuleId: "deny-private-v4"},
			{Action: "deny", IpVersion: 6, DstNet: []string{"2001:5a8:4295:b601::/64", "2001:5a8:4298:3b01::/64", "::/128", "::1/128", "::ffff:0:0/96", "64:ff9b::/96", "fc00::/7", "fe80::/10", "ff00::/8"}, RuleId: "deny-private-v6"},
			{Action: "allow", IpVersion: 4, Protocol: udp, DstNet: []string{"0.0.0.0/0"}, RuleId: "wan-v4-udp"},
			{Action: "allow", IpVersion: 4, Protocol: tcp, DstNet: []string{"0.0.0.0/0"}, RuleId: "wan-v4-tcp"},
			{Action: "allow", IpVersion: 6, Protocol: udp, DstNet: []string{"2000::/3"}, RuleId: "wan-v6-udp"},
			{Action: "allow", IpVersion: 6, Protocol: tcp, DstNet: []string{"2000::/3"}, RuleId: "wan-v6-tcp"},
			{Action: "deny", RuleId: "default-deny"},
		},
	})
	ps.AddOrReplacePolicySet("policy-default-allow", &proto.Policy{
		InboundRules:  []*proto.Rule{{Action: "allow", RuleId: "in"}},
		OutboundRules: []*proto.Rule{{Action: "allow", RuleId: "out"}},
	})
	ps.AddOrReplacePolicySet("policy-default-icmp-allow", &proto.Policy{
		InboundRules:  []*proto.Rule{{Action: "allow", Protocol: icmp, RuleId: "in-icmp"}, {Action: "allow", Protocol: icmpv6, RuleId: "in-icmpv6"}},
		OutboundRules: []*proto.Rule{{Action: "allow", Protocol: icmp, RuleId: "out-icmp"}, {Action: "allow", Protocol: icmpv6, RuleId: "out-icmpv6"}},
	})
	ps.AddOrReplacePolicySet("policy-default-tcp-udp-allow", &proto.Policy{
		InboundRules:  []*proto.Rule{{Action: "allow", Protocol: udp, RuleId: "in-udp"}, {Action: "allow", Protocol: tcp, RuleId: "in-tcp"}},
		OutboundRules: []*proto.Rule{{Action: "allow", Protocol: udp, RuleId: "out-udp"}, {Action: "allow", Protocol: tcp, RuleId: "out-tcp"}},
	})

	egressTier := ps.GetPolicySetRules([]string{
		"policy-appmana-unity-runtime-egress",
		"policy-default-allow",
		"policy-default-icmp-allow",
		"policy-default-tcp-udp-allow",
	}, false, true)
	egressRules := flattenTiers([][]*hns.ACLPolicy{egressTier})
	rewritePriorities(egressRules, policysets.PolicyRuleMaxPriority)

	for _, rule := range egressRules {
		Expect(ruleHasMixedAddressFamilies(rule)).To(BeFalse(), "live appmana egress policy must not emit mixed-family ACLs: %+v", rule)
		Expect(ruleLocalAndRemoteFamiliesCompatible(rule)).To(BeTrue(), "live appmana egress policy must not cross local/remote families: %+v", rule)
	}

	Expect(rulesContainProtocol(egressRules, 1)).To(BeTrue(), "expected ICMP from default-icmp-allow")
	Expect(rulesContainProtocol(egressRules, 58)).To(BeTrue(), "expected ICMPv6 from default-icmp-allow")
	Expect(rulesContainRemoteAddresses(egressRules, "0.0.0.0/0")).To(BeTrue(), "expected IPv4 WAN allow")
	Expect(rulesContainRemoteAddresses(egressRules, "2000::/3")).To(BeTrue(), "expected IPv6 WAN allow")
}

func ruleHasMixedAddressFamilies(rule *hns.ACLPolicy) bool {
	return addressListHasMixedFamilies(rule.LocalAddresses) || addressListHasMixedFamilies(rule.RemoteAddresses)
}

func ruleLocalAndRemoteFamiliesCompatible(rule *hns.ACLPolicy) bool {
	localFamily := addressListFamily(rule.LocalAddresses)
	remoteFamily := addressListFamily(rule.RemoteAddresses)
	return addressFamiliesCompatible(localFamily, remoteFamily)
}

func addressListHasMixedFamilies(addresses string) bool {
	if addresses == "" {
		return false
	}
	var hasV4, hasV6 bool
	for _, address := range strings.Split(addresses, ",") {
		if isIPv6AddressOrCIDR(address) {
			hasV6 = true
		} else {
			hasV4 = true
		}
	}
	return hasV4 && hasV6
}

func rulesContainProtocol(rules []*hns.ACLPolicy, protocol uint16) bool {
	for _, rule := range rules {
		if rule.Protocol == protocol {
			return true
		}
	}
	return false
}

func rulesContainRemoteAddresses(rules []*hns.ACLPolicy, addresses string) bool {
	for _, rule := range rules {
		if rule.RemoteAddresses == addresses {
			return true
		}
	}
	return false
}
