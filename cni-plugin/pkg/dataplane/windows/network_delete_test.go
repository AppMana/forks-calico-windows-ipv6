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

type endpointDeletionMock struct {
	detachErr, deleteErr, lookupErr, listErr error
	byName                                   *namespaceCleanupEndpoint
	inNamespace                              []namespaceCleanupEndpoint
	calls                                    []string
	attachments                              map[string]bool
	orphaned                                 bool
}

func (m *endpointDeletionMock) detach(ns, ep string) error {
	m.calls = append(m.calls, "detach:"+ns+":"+ep)
	if m.detachErr == nil {
		delete(m.attachments, ep)
	}
	return m.detachErr
}

func (m *endpointDeletionMock) deleteEndpoint(id string) error {
	m.calls = append(m.calls, "delete-ep:"+id)
	if m.attachments[id] {
		m.orphaned = true
	}
	return m.deleteErr
}

func (m *endpointDeletionMock) endpointByName(name string) (*namespaceCleanupEndpoint, error) {
	m.calls = append(m.calls, "by-name:"+name)
	return m.byName, m.lookupErr
}

func (m *endpointDeletionMock) namespaceEndpoints(ns string) ([]namespaceCleanupEndpoint, error) {
	m.calls = append(m.calls, "ns-endpoints:"+ns)
	return m.inNamespace, m.listErr
}

func TestDeleteEndpointDetachesBeforeDeleteAndReproducesOrphanWithoutDetach(t *testing.T) {
	m := &endpointDeletionMock{attachments: map[string]bool{"ep-1": true}}
	if err := deleteEndpointWithDetach("ep-1", "ns-1", m); err != nil {
		t.Fatal(err)
	}
	if m.orphaned || !reflect.DeepEqual(m.calls, []string{"detach:ns-1:ep-1", "delete-ep:ep-1"}) {
		t.Fatalf("endpoint must be detached before deletion: %+v", m)
	}

	// Unattached or zero-GUID endpoints skip namespace detach cleanly.
	for _, unattached := range []string{"", "00000000-0000-0000-0000-000000000000"} {
		u := &endpointDeletionMock{}
		if err := deleteEndpointWithDetach("host-ep", unattached, u); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(u.calls, []string{"delete-ep:host-ep"}) {
			t.Fatalf("unexpected calls for unattached endpoint (%q): %v", unattached, u.calls)
		}
	}

	// Negative control: raw deleteEndpoint while attached leaves an orphan reference.
	raw := &endpointDeletionMock{attachments: map[string]bool{"ep-1": true}}
	_ = raw.deleteEndpoint("ep-1")
	if !raw.orphaned {
		t.Fatal("negative control did not reproduce orphan reference")
	}

	// Fail closed when detach fails: never delete an attached endpoint.
	failDetach := &endpointDeletionMock{
		detachErr:   errors.New("detach failed"),
		attachments: map[string]bool{"ep-1": true},
	}
	if err := deleteEndpointWithDetach("ep-1", "ns-1", failDetach); err == nil {
		t.Fatal("expected detach error")
	}
	if !reflect.DeepEqual(failDetach.calls, []string{"detach:ns-1:ep-1"}) {
		t.Fatalf("endpoint must not be deleted when detach fails: %v", failDetach.calls)
	}
}

func TestCleanUpNamespaceEndpointsDetachesResidualAndPropagatesErrors(t *testing.T) {
	// Primary named endpoint + residual unnamed/stale endpoint on same Calico network.
	m := &endpointDeletionMock{
		byName: &namespaceCleanupEndpoint{id: "ep-primary", name: "sbx_Calico", networkName: "Calico", namespaceID: "ns-1"},
		inNamespace: []namespaceCleanupEndpoint{
			{id: "ep-primary", name: "sbx_Calico", networkName: "Calico", namespaceID: "ns-1"},
			{id: "ep-residual", name: "stale_Calico", networkName: "Calico", namespaceID: "ns-1"},
			{id: "ep-foreign", name: "other_Ext", networkName: "OtherNet", namespaceID: "ns-1"},
		},
		attachments: map[string]bool{"ep-primary": true, "ep-residual": true},
	}
	if err := cleanUpNamespaceEndpoints("sbx_Calico", "ns-1", "Calico", m); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"by-name:sbx_Calico",
		"detach:ns-1:ep-primary",
		"delete-ep:ep-primary",
		"ns-endpoints:ns-1",
		"detach:ns-1:ep-residual",
		"delete-ep:ep-residual",
	}
	if m.orphaned || !reflect.DeepEqual(m.calls, want) {
		t.Fatalf("unexpected cleanup sequence: calls=%v orphaned=%v", m.calls, m.orphaned)
	}

	// Errors must never be swallowed as nil (which previously caused containerd to
	// call HcnDeleteNamespace on a namespace with a still-attached endpoint -> 0x803b0015).
	boom := errors.New("hcn failure")
	for _, tc := range []struct {
		name string
		mock endpointDeletionMock
	}{
		{"lookup-err", endpointDeletionMock{lookupErr: boom}},
		{"detach-err", endpointDeletionMock{byName: &namespaceCleanupEndpoint{id: "ep", name: "sbx_Calico", namespaceID: "ns-1"}, detachErr: boom}},
		{"delete-err", endpointDeletionMock{byName: &namespaceCleanupEndpoint{id: "ep", name: "sbx_Calico", namespaceID: "ns-1"}, deleteErr: boom}},
		{"ns-list-err", endpointDeletionMock{listErr: boom}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := cleanUpNamespaceEndpoints("sbx_Calico", "ns-1", "Calico", &tc.mock); err == nil {
				t.Fatal("cleanup error was swallowed")
			}
		})
	}
}
