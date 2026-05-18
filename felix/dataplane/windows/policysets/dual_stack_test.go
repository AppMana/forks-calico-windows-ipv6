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

package policysets

import (
	"strings"
	"testing"

	. "github.com/onsi/gomega"

	"github.com/projectcalico/calico/felix/dataplane/windows/hns"
	"github.com/projectcalico/calico/felix/proto"
)

// Verify that ipVersion is set to 0 (dual-stack).
func TestIpVersionIsDualStack(t *testing.T) {
	RegisterTestingT(t)
	Expect(ipVersion).To(Equal(uint8(0)), "ipVersion should be 0 for dual-stack")
}

// filterNets with ipVersion=0 should pass all CIDRs through.
func TestFilterNets_DualStack_PassesAll(t *testing.T) {
	RegisterTestingT(t)
	mixed := []string{"10.0.0.0/24", "fd00::/64", "192.168.1.0/26", "2001:db8::/32"}
	filtered, filteredAll := filterNets(mixed, 0)
	Expect(filteredAll).To(BeFalse())
	Expect(filtered).To(Equal(mixed))
}

// filterNets with ipVersion=4 should only pass IPv4.
func TestFilterNets_V4Only(t *testing.T) {
	RegisterTestingT(t)
	mixed := []string{"10.0.0.0/24", "fd00::/64", "192.168.1.0/26"}
	filtered, filteredAll := filterNets(mixed, 4)
	Expect(filteredAll).To(BeFalse())
	Expect(filtered).To(Equal([]string{"10.0.0.0/24", "192.168.1.0/26"}))
}

// filterNets with ipVersion=6 should only pass IPv6.
func TestFilterNets_V6Only(t *testing.T) {
	RegisterTestingT(t)
	mixed := []string{"10.0.0.0/24", "fd00::/64", "2001:db8::/32"}
	filtered, filteredAll := filterNets(mixed, 6)
	Expect(filteredAll).To(BeFalse())
	Expect(filtered).To(Equal([]string{"fd00::/64", "2001:db8::/32"}))
}

// filterNets with ipVersion=4 and only IPv6 CIDRs should return filteredAll=true.
func TestFilterNets_V4Only_AllFiltered(t *testing.T) {
	RegisterTestingT(t)
	v6only := []string{"fd00::/64", "2001:db8::/32"}
	filtered, filteredAll := filterNets(v6only, 4)
	Expect(filteredAll).To(BeTrue())
	Expect(filtered).To(BeEmpty())
}

// filterNets with empty input should return nil, false.
func TestFilterNets_Empty(t *testing.T) {
	RegisterTestingT(t)
	filtered, filteredAll := filterNets(nil, 0)
	Expect(filteredAll).To(BeFalse())
	Expect(filtered).To(BeNil())
}

func TestSplitIPListByFamily(t *testing.T) {
	RegisterTestingT(t)

	Expect(SplitIPListByFamily(nil, 2)).To(Equal([]AddressFamilyChunks{{Chunks: [][]string{{}}}}))
	Expect(SplitIPListByFamily([]string{"10.0.0.0/24", "10.1.0.0/24", "fd00::/64", "2001:db8::/64", "10.2.0.0/24"}, 2)).To(Equal([]AddressFamilyChunks{
		{
			Family: 4,
			Chunks: [][]string{
				{"10.0.0.0/24", "10.1.0.0/24"},
				{"10.2.0.0/24"},
			},
		},
		{
			Family: 6,
			Chunks: [][]string{
				{"fd00::/64", "2001:db8::/64"},
			},
		},
	}))
}

// Verify that a policy with IPv6 source CIDR produces rules (not skipped as no-op).
func TestDualStack_IPv6RulesNotSkipped(t *testing.T) {
	RegisterTestingT(t)
	h := mockHNS{}
	h.SupportedFeatures.Acl.AclRuleId = true
	h.SupportedFeatures.Acl.AclNoHostRulePriority = true

	ipsc := mockIPSetCache{IPSets: map[string][]string{}}
	ps := NewPolicySets(&h, []IPSetCache{&ipsc}, mockReader(""))

	ps.AddOrReplacePolicySet("test-v6-policy", &proto.Policy{
		InboundRules: []*proto.Rule{
			{
				Action:    "allow",
				IpVersion: 6,
				SrcNet:    []string{"fd00:10:3::/48"},
			},
		},
		OutboundRules: []*proto.Rule{},
	})

	rules := ps.GetPolicySetRules([]string{"test-v6-policy"}, true, true)
	// Should have at least 2 rules: the IPv6 allow rule + the end-of-tier drop.
	Expect(len(rules)).To(BeNumerically(">=", 2), "IPv6 rules should not be filtered out")

	// Find the allow rule with the IPv6 source.
	found := false
	for _, r := range rules {
		if r.Action == hns.Allow && r.LocalAddresses == "fd00:10:3::/48" {
			found = true
			break
		}
	}
	// Note: the source CIDR may be in RemoteAddresses for inbound. Check both fields.
	if !found {
		for _, r := range rules {
			if r.Action == hns.Allow && r.RemoteAddresses == "fd00:10:3::/48" {
				found = true
				break
			}
		}
	}
	Expect(found).To(BeTrue(), "Expected an allow rule with IPv6 source fd00:10:3::/48 in rules: %+v", rules)
}

// Verify that a policy with mixed IPv4 and IPv6 CIDRs keeps both address
// families but emits them as separate HNS ACLs.
func TestDualStack_MixedCIDRsSplitByFamily(t *testing.T) {
	RegisterTestingT(t)
	h := mockHNS{}
	h.SupportedFeatures.Acl.AclRuleId = true
	h.SupportedFeatures.Acl.AclNoHostRulePriority = true

	ipsc := mockIPSetCache{IPSets: map[string][]string{}}
	ps := NewPolicySets(&h, []IPSetCache{&ipsc}, mockReader(""))

	ps.AddOrReplacePolicySet("test-mixed", &proto.Policy{
		InboundRules: []*proto.Rule{
			{
				Action: "allow",
				SrcNet: []string{"10.0.0.0/8", "fd00::/16"},
			},
		},
		OutboundRules: []*proto.Rule{},
	})

	rules := ps.GetPolicySetRules([]string{"test-mixed"}, true, true)
	foundV4 := false
	foundV6 := false
	for _, r := range rules {
		if r.Action != hns.Allow {
			continue
		}
		switch r.RemoteAddresses {
		case "10.0.0.0/8":
			foundV4 = true
		case "fd00::/16":
			foundV6 = true
		}
	}
	Expect(foundV4).To(BeTrue(), "Expected separate IPv4 allow rule, got: %+v", rules)
	Expect(foundV6).To(BeTrue(), "Expected separate IPv6 allow rule, got: %+v", rules)
	for _, r := range rules {
		Expect(ruleHasMixedAddressFamilies(r)).To(BeFalse(), "HNS ACLs must not mix IPv4 and IPv6 addresses: %+v", r)
	}
}

func TestDualStack_EgressNetworkSetSplitByFamily(t *testing.T) {
	RegisterTestingT(t)
	h := mockHNS{}
	h.SupportedFeatures.Acl.AclRuleId = true
	h.SupportedFeatures.Acl.AclNoHostRulePriority = true

	ipsc := mockIPSetCache{IPSets: map[string][]string{
		"blocked-egress": {
			"10.3.0.0/16",
			"10.152.184.0/24",
			"172.16.0.0/12",
			"192.168.0.0/16",
			"2001:5a8:4295:b601::/64",
			"2001:5a8:4298:3b01::/64",
			"fc00::/7",
			"fe80::/10",
		},
	}}
	ps := NewPolicySets(&h, []IPSetCache{&ipsc}, mockReader(""))

	ps.AddOrReplacePolicySet("egress-networkset-policy", &proto.Policy{
		OutboundRules: []*proto.Rule{
			{
				Action:      "deny",
				DstIpSetIds: []string{"blocked-egress"},
				RuleId:      "deny-cluster-private-special",
			},
			{
				Action:    "allow",
				IpVersion: 4,
				Protocol:  &proto.Protocol{NumberOrName: &proto.Protocol_Name{Name: "tcp"}},
				DstNet:    []string{"0.0.0.0/0"},
				RuleId:    "allow-public-v4-tcp",
			},
			{
				Action:    "allow",
				IpVersion: 6,
				Protocol:  &proto.Protocol{NumberOrName: &proto.Protocol_Name{Name: "tcp"}},
				DstNet:    []string{"2000::/3"},
				RuleId:    "allow-public-v6-tcp",
			},
			{
				Action: "deny",
				RuleId: "default-deny",
			},
		},
	})

	rules := ps.GetPolicySetRules([]string{"egress-networkset-policy"}, false, true)
	var foundBlockedV4, foundBlockedV6 bool
	for _, r := range rules {
		Expect(ruleHasMixedAddressFamilies(r)).To(BeFalse(), "HNS ACLs must not mix IPv4 and IPv6 addresses: %+v", r)
		if r.Action == hns.Block && r.RemoteAddresses == "10.3.0.0/16,10.152.184.0/24,172.16.0.0/12,192.168.0.0/16" {
			foundBlockedV4 = true
		}
		if r.Action == hns.Block && r.RemoteAddresses == "2001:5a8:4295:b601::/64,2001:5a8:4298:3b01::/64,fc00::/7,fe80::/10" {
			foundBlockedV6 = true
		}
	}
	Expect(foundBlockedV4).To(BeTrue(), "Expected blocked IPv4 set rendered as its own HNS ACL, got: %+v", rules)
	Expect(foundBlockedV6).To(BeTrue(), "Expected blocked IPv6 set rendered as its own HNS ACL, got: %+v", rules)
}

func TestDualStack_ProductRuleMatrixNeverMixesFamilies(t *testing.T) {
	RegisterTestingT(t)

	tcp := &proto.Protocol{NumberOrName: &proto.Protocol_Name{Name: "tcp"}}
	udp := &proto.Protocol{NumberOrName: &proto.Protocol_Name{Name: "udp"}}
	cases := []struct {
		name        string
		rule        *proto.Rule
		inbound     bool
		ipsets      map[string][]string
		wantV4      bool
		wantV6      bool
		wantRuleCnt int
		wantNoRule  bool
	}{
		{
			name:        "inbound source cidrs with tcp ports",
			inbound:     true,
			rule:        &proto.Rule{Action: "allow", SrcNet: []string{"10.0.0.0/24", "fd00::/64"}, Protocol: tcp, SrcPorts: []*proto.PortRange{{First: 1000, Last: 1001}}, DstPorts: []*proto.PortRange{{First: 443, Last: 443}}, RuleId: "rule"},
			wantV4:      true,
			wantV6:      true,
			wantRuleCnt: 2,
		},
		{
			name:        "inbound destination cidrs with udp ports",
			inbound:     true,
			rule:        &proto.Rule{Action: "deny", DstNet: []string{"10.3.0.0/16", "2001:db8:3::/64"}, Protocol: udp, DstPorts: []*proto.PortRange{{First: 53, Last: 53}}, RuleId: "rule"},
			wantV4:      true,
			wantV6:      true,
			wantRuleCnt: 2,
		},
		{
			name:        "outbound destination cidrs with tcp ports",
			inbound:     false,
			rule:        &proto.Rule{Action: "allow", DstNet: []string{"0.0.0.0/0", "2000::/3"}, Protocol: tcp, DstPorts: []*proto.PortRange{{First: 443, Last: 443}}, RuleId: "rule"},
			wantV4:      true,
			wantV6:      true,
			wantRuleCnt: 2,
		},
		{
			name:        "outbound source cidrs",
			inbound:     false,
			rule:        &proto.Rule{Action: "allow", SrcNet: []string{"10.3.0.0/16", "2001:db8:3::/64"}, RuleId: "rule"},
			wantV4:      true,
			wantV6:      true,
			wantRuleCnt: 2,
		},
		{
			name:        "source and destination mixed cidrs only pair same families",
			inbound:     false,
			rule:        &proto.Rule{Action: "allow", SrcNet: []string{"10.3.0.0/16", "2001:db8:3::/64"}, DstNet: []string{"0.0.0.0/0", "2000::/3"}, RuleId: "rule"},
			wantV4:      true,
			wantV6:      true,
			wantRuleCnt: 2,
		},
		{
			name:        "source ipset mixed",
			inbound:     true,
			rule:        &proto.Rule{Action: "allow", SrcIpSetIds: []string{"mixed-src"}, RuleId: "rule"},
			ipsets:      map[string][]string{"mixed-src": {"10.0.0.1", "10.0.0.2", "fd00::1", "fd00::2"}},
			wantV4:      true,
			wantV6:      true,
			wantRuleCnt: 2,
		},
		{
			name:        "destination ipset mixed",
			inbound:     false,
			rule:        &proto.Rule{Action: "deny", DstIpSetIds: []string{"mixed-dst"}, RuleId: "rule"},
			ipsets:      map[string][]string{"mixed-dst": {"10.152.184.10/32", "10.152.184.30/32", "fd00:10:152::10/128"}},
			wantV4:      true,
			wantV6:      true,
			wantRuleCnt: 2,
		},
		{
			name:        "source and destination ipsets mixed only pair same families",
			inbound:     true,
			rule:        &proto.Rule{Action: "allow", SrcIpSetIds: []string{"mixed-src"}, DstIpSetIds: []string{"mixed-dst"}, RuleId: "rule"},
			ipsets:      map[string][]string{"mixed-src": {"10.0.0.1", "fd00::1"}, "mixed-dst": {"10.1.0.1", "fd00:1::1"}},
			wantV4:      true,
			wantV6:      true,
			wantRuleCnt: 2,
		},
		{
			name:        "destination ip-port set mixed",
			inbound:     false,
			rule:        &proto.Rule{Action: "allow", DstIpPortSetIds: []string{"mixed-ipports"}, RuleId: "rule"},
			ipsets:      map[string][]string{"mixed-ipports": {"10.1.0.10,tcp:443", "10.1.0.11,tcp:443", "2001:db8::10,tcp:443"}},
			wantV4:      true,
			wantV6:      true,
			wantRuleCnt: 2,
		},
		{
			name:        "ipv4 rule filters mixed cidrs to v4",
			inbound:     false,
			rule:        &proto.Rule{Action: "allow", IpVersion: 4, DstNet: []string{"0.0.0.0/0", "2000::/3"}, RuleId: "rule"},
			wantV4:      true,
			wantRuleCnt: 1,
		},
		{
			name:        "ipv6 rule filters mixed cidrs to v6",
			inbound:     false,
			rule:        &proto.Rule{Action: "allow", IpVersion: 6, DstNet: []string{"0.0.0.0/0", "2000::/3"}, RuleId: "rule"},
			wantV6:      true,
			wantRuleCnt: 1,
		},
		{
			name:       "ipv4 rule with only ipv6 cidrs is no-op",
			inbound:    false,
			rule:       &proto.Rule{Action: "allow", IpVersion: 4, DstNet: []string{"2000::/3"}, RuleId: "rule"},
			wantNoRule: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			RegisterTestingT(t)
			ps := newDualStackPolicySets(tc.ipsets)
			rules, err := ps.protoRuleToHnsRules("matrix", tc.rule, tc.inbound, 2)
			if tc.wantNoRule {
				Expect(err).To(Equal(ErrRuleIsNoOp))
				Expect(rules).To(BeNil())
				return
			}
			Expect(err).NotTo(HaveOccurred())
			Expect(rules).To(HaveLen(tc.wantRuleCnt))
			assertNoMixedAddressFamilyRules(rules)
			Expect(rulesContainFamily(rules, 4)).To(Equal(tc.wantV4), "rules: %+v", rules)
			Expect(rulesContainFamily(rules, 6)).To(Equal(tc.wantV6), "rules: %+v", rules)
			for _, r := range rules {
				Expect(ruleLocalAndRemoteFamiliesCompatible(r)).To(BeTrue(), "local/remote families must not cross: %+v", r)
				Expect(r.Direction == hns.In).To(Equal(tc.inbound), "unexpected direction in rule: %+v", r)
			}
		})
	}
}

func TestDualStack_ProductSetPolicyOrderingAndDefaults(t *testing.T) {
	RegisterTestingT(t)

	ps := newDualStackPolicySets(map[string][]string{
		"blocked": {
			"10.0.0.0/8",
			"172.16.0.0/12",
			"fd00::/8",
			"fe80::/10",
		},
	})
	ps.AddOrReplacePolicySet("egress", &proto.Policy{
		OutboundRules: []*proto.Rule{
			{Action: "allow", Protocol: &proto.Protocol{NumberOrName: &proto.Protocol_Name{Name: "udp"}}, DstNet: []string{"10.152.184.10/32"}, DstPorts: []*proto.PortRange{{First: 53, Last: 53}}, RuleId: "dns-udp"},
			{Action: "deny", DstIpSetIds: []string{"blocked"}, RuleId: "deny-blocked"},
			{Action: "allow", IpVersion: 4, Protocol: &proto.Protocol{NumberOrName: &proto.Protocol_Name{Name: "tcp"}}, DstNet: []string{"0.0.0.0/0"}, RuleId: "public-v4-tcp"},
			{Action: "allow", IpVersion: 6, Protocol: &proto.Protocol{NumberOrName: &proto.Protocol_Name{Name: "tcp"}}, DstNet: []string{"2000::/3"}, RuleId: "public-v6-tcp"},
			{Action: "deny", RuleId: "default-deny"},
		},
	})

	rules := ps.GetPolicySetRules([]string{"egress"}, false, true)
	assertNoMixedAddressFamilyRules(rules)
	Expect(rulesContainFamily(rules, 4)).To(BeTrue())
	Expect(rulesContainFamily(rules, 6)).To(BeTrue())
	Expect(rules).To(ContainElement(&hns.ACLPolicy{
		Type: hns.ACL, Id: "egress-deny-blocked-0", Protocol: 256, Action: hns.Block,
		Direction: hns.Out, RemoteAddresses: "10.0.0.0/8,172.16.0.0/12", RuleType: hns.Switch, Priority: 1001,
	}))
	Expect(rules).To(ContainElement(&hns.ACLPolicy{
		Type: hns.ACL, Id: "egress-deny-blocked-1", Protocol: 256, Action: hns.Block,
		Direction: hns.Out, RemoteAddresses: "fd00::/8,fe80::/10", RuleType: hns.Switch, Priority: 1001,
	}))
	Expect(rules[len(rules)-1]).To(Equal(&hns.ACLPolicy{
		Type: hns.ACL, Protocol: 256, Action: hns.Block, Direction: hns.Out, RuleType: hns.Switch, Priority: 1004,
	}))
}

// Verify that an IPv4-only rule with IpVersion=4 is still accepted (not rejected).
func TestDualStack_IPv4RuleStillWorks(t *testing.T) {
	RegisterTestingT(t)
	h := mockHNS{}
	h.SupportedFeatures.Acl.AclRuleId = true
	h.SupportedFeatures.Acl.AclNoHostRulePriority = true

	ipsc := mockIPSetCache{IPSets: map[string][]string{}}
	ps := NewPolicySets(&h, []IPSetCache{&ipsc}, mockReader(""))

	ps.AddOrReplacePolicySet("test-v4", &proto.Policy{
		InboundRules: []*proto.Rule{
			{
				Action:    "allow",
				IpVersion: 4,
				SrcNet:    []string{"10.0.0.0/8"},
			},
		},
		OutboundRules: []*proto.Rule{},
	})

	rules := ps.GetPolicySetRules([]string{"test-v4"}, true, true)
	found := false
	for _, r := range rules {
		if r.Action == hns.Allow {
			found = true
			break
		}
	}
	Expect(found).To(BeTrue(), "IPv4 rule should still be accepted in dual-stack mode")
}

func ruleHasMixedAddressFamilies(rule *hns.ACLPolicy) bool {
	return addressListHasMixedFamilies(rule.LocalAddresses) || addressListHasMixedFamilies(rule.RemoteAddresses)
}

func assertNoMixedAddressFamilyRules(rules []*hns.ACLPolicy) {
	for _, rule := range rules {
		Expect(ruleHasMixedAddressFamilies(rule)).To(BeFalse(), "HNS ACLs must not mix IPv4 and IPv6 addresses: %+v", rule)
	}
}

func rulesContainFamily(rules []*hns.ACLPolicy, family uint8) bool {
	for _, rule := range rules {
		if addressListContainsFamily(rule.LocalAddresses, family) || addressListContainsFamily(rule.RemoteAddresses, family) {
			return true
		}
	}
	return false
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

func addressListContainsFamily(addresses string, family uint8) bool {
	if addresses == "" {
		return false
	}
	for _, address := range strings.Split(addresses, ",") {
		isV6 := isIPv6AddressOrCIDR(address)
		if family == 6 && isV6 {
			return true
		}
		if family == 4 && !isV6 {
			return true
		}
	}
	return false
}

func addressListFamily(addresses string) uint8 {
	if addresses == "" {
		return 0
	}
	if addressListContainsFamily(addresses, 6) {
		return 6
	}
	return 4
}

func newDualStackPolicySets(ipsets map[string][]string) *PolicySets {
	h := mockHNS{}
	h.SupportedFeatures.Acl.AclRuleId = true
	h.SupportedFeatures.Acl.AclNoHostRulePriority = true
	return NewPolicySets(&h, []IPSetCache{&mockIPSetCache{IPSets: ipsets}}, mockReader(""))
}
