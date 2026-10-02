package windows

import (
	"errors"
	"reflect"
	"testing"
)

type deletionMock struct {
	items                         []networkDeletionEndpoint
	listErr, detachErr, deleteErr error
	calls                         []string
	attachments                   map[string]bool
	orphaned                      bool
}

func (m *deletionMock) endpoints(id string) ([]networkDeletionEndpoint, error) {
	m.calls = append(m.calls, "list:"+id)
	return m.items, m.listErr
}

func (m *deletionMock) detach(ns, ep string) error {
	m.calls = append(m.calls, "detach:"+ns+":"+ep)
	if m.detachErr == nil {
		delete(m.attachments, ep)
	}
	return m.detachErr
}

func (m *deletionMock) deleteNetwork(id string) error {
	m.calls = append(m.calls, "delete:"+id)
	m.orphaned = len(m.attachments) != 0
	return m.deleteErr
}

func TestDeleteNetworkDetachesBeforeEndpointsDisappear(t *testing.T) {
	m := &deletionMock{
		items:       []networkDeletionEndpoint{{"pod-ep", "NETWORK", "pod-ns"}, {"host-ep", "network", ""}},
		attachments: map[string]bool{"pod-ep": true},
	}
	if err := deleteNetworkWithEndpoints("network", m); err != nil {
		t.Fatal(err)
	}
	if m.orphaned || !reflect.DeepEqual(m.calls, []string{"list:network", "detach:pod-ns:pod-ep", "delete:network"}) {
		t.Fatalf("namespace references must be removed before network deletion: %+v", m)
	}
	// Negative control: network deletion alone models the native HNS result
	// reproduced by TestLabNetworkDeleteRemovesNamespaceReferences.
	old := &deletionMock{attachments: map[string]bool{"pod-ep": true}}
	_ = old.deleteNetwork("network")
	if !old.orphaned {
		t.Fatal("model did not reproduce the measured orphan reference")
	}
}

func TestDeleteNetworkFailsClosed(t *testing.T) {
	boom := errors.New("native failure")
	for _, tc := range []struct {
		name string
		mock deletionMock
		want []string
	}{
		{"list", deletionMock{listErr: boom}, []string{"list:network"}},
		{"detach", deletionMock{items: []networkDeletionEndpoint{{"ep", "network", "ns"}}, detachErr: boom}, []string{"list:network", "detach:ns:ep"}},
		{"foreign", deletionMock{items: []networkDeletionEndpoint{{"ep", "foreign-network", "ns"}}}, []string{"list:network"}},
		{"missing endpoint identity", deletionMock{items: []networkDeletionEndpoint{{"", "network", "ns"}}}, []string{"list:network"}},
		{"delete", deletionMock{deleteErr: boom}, []string{"list:network", "delete:network"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := deleteNetworkWithEndpoints("network", &tc.mock); err == nil {
				t.Fatal("native failure or invalid identity was ignored")
			}
			if !reflect.DeepEqual(tc.mock.calls, tc.want) {
				t.Fatalf("unexpected operations: %v", tc.mock.calls)
			}
		})
	}
	m := &deletionMock{}
	if err := deleteNetworkWithEndpoints("", m); err == nil || len(m.calls) != 0 {
		t.Fatal("missing network identity must fail before any API access")
	}
}
