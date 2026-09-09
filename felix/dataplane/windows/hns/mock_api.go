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

package hns

// MockAPI is a configurable mock of the HNS API for testing.
type MockAPI struct {
	ACLUpdates        map[string][]ACLPolicy
	ApplyError        error
	Endpoints         []HNSEndpoint
	SupportedFeatures HNSSupportedFeatures
}

func (m *MockAPI) GetHNSSupportedFeatures() HNSSupportedFeatures {
	return m.SupportedFeatures
}

func (m *MockAPI) HNSListEndpointRequest() ([]HNSEndpoint, error) {
	if m.Endpoints == nil {
		return []HNSEndpoint{}, nil
	}
	return m.Endpoints, nil
}

func (m *MockAPI) ApplyACLPolicy(id string, rules ...*ACLPolicy) error {
	if m.ApplyError != nil {
		return m.ApplyError
	}
	if m.ACLUpdates == nil {
		m.ACLUpdates = map[string][]ACLPolicy{}
	}
	copied := make([]ACLPolicy, len(rules))
	for i, rule := range rules {
		copied[i] = *rule
	}
	m.ACLUpdates[id] = copied
	return nil
}
