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

// Verify that a policy with mixed IPv4 and IPv6 CIDRs keeps both.
func TestDualStack_MixedCIDRsPreserved(t *testing.T) {
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
	found := false
	for _, r := range rules {
		if r.Action == hns.Allow && r.RemoteAddresses == "10.0.0.0/8,fd00::/16" {
			found = true
			break
		}
	}
	Expect(found).To(BeTrue(), "Expected allow rule with both IPv4 and IPv6 CIDRs, got: %+v", rules)
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
