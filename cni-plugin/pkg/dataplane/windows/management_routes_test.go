package windows

import (
	"fmt"
	"testing"

	"github.com/sirupsen/logrus"
)

// Network deletion models the real Server 2022 failure: the management IP
// survives a prefix-driven L2Bridge replacement, but its configured route does
// not. These tests call the production reconciliation path, not a repair loop.
type routeLosingHNS struct {
	*mockHNS
	route, checkpoint         string
	beginErr, completeErr     error
	beginCalls, completeCalls int
}

func (m *routeLosingHNS) Delete(n *HNSNetworkInfo) error {
	if err := m.mockHNS.Delete(n); err != nil {
		return err
	}
	m.route = ""
	return nil
}

func (m *routeLosingHNS) BeginManagementRouteTransition(*logrus.Entry) error {
	m.beginCalls++
	if m.beginErr != nil {
		return m.beginErr
	}
	if m.checkpoint == "" {
		m.checkpoint = m.route
	}
	return nil
}

func (m *routeLosingHNS) CompleteManagementRouteTransition(*logrus.Entry) error {
	m.completeCalls++
	if m.completeErr != nil {
		return m.completeErr
	}
	if m.checkpoint != "" {
		m.route = m.checkpoint
		m.checkpoint = ""
	}
	return nil
}

func newRouteLosingHNS() *routeLosingHNS {
	m := &routeLosingHNS{mockHNS: newMockHNS(), route: "10.96.0.0/12 via 192.0.2.10 metric 42"}
	m.networks["Calico"] = &HNSNetworkInfo{Name: "Calico", Type: "L2Bridge", Subnets: []HNSSubnet{
		{AddressPrefix: "10.3.16.0/26", GatewayAddress: "10.3.16.1"},
		{AddressPrefix: "2001:db8:1::/122", GatewayAddress: "2001:db8:1::1"},
	}}
	return m
}

func TestPrefixReplacementPreservesManagementRoute(t *testing.T) {
	m := newRouteLosingHNS()
	want := m.route
	_, err := ensureNetworkExistsWithAPI("Calico", mustParseCIDR("10.3.16.0/26"), mustParseCIDR("2001:db8:2::/122"), "", "", testLogger(), m)
	if err != nil {
		t.Fatal(err)
	}
	if m.deleteCalls == 0 || m.createCalls == 0 {
		t.Fatal("test did not replace the network")
	}
	if m.route != want {
		t.Fatalf("CNI prefix replacement lost administrator route: got %q, want %q", m.route, want)
	}
	if m.checkpoint != "" {
		t.Fatal("successful restoration left a pending checkpoint")
	}
}

func TestPrefixReplacementCannotDeleteWithoutRouteCheckpoint(t *testing.T) {
	m := newRouteLosingHNS()
	m.beginErr = fmt.Errorf("checkpoint disk is full")
	_, err := ensureNetworkExistsWithAPI("Calico", mustParseCIDR("10.3.16.0/26"), mustParseCIDR("2001:db8:2::/122"), "", "", testLogger(), m)
	if err == nil || m.deleteCalls != 0 || m.createCalls != 0 || m.route == "" {
		t.Fatalf("must fail before mutation: err=%v deletes=%d creates=%d route=%q", err, m.deleteCalls, m.createCalls, m.route)
	}
}

func TestPrefixReplacementRetriesPendingRouteRestoration(t *testing.T) {
	m := newRouteLosingHNS()
	want := m.route
	m.completeErr = fmt.Errorf("management interface not ready")
	_, err := ensureNetworkExistsWithAPI("Calico", mustParseCIDR("10.3.16.0/26"), mustParseCIDR("2001:db8:2::/122"), "", "", testLogger(), m)
	if err == nil || m.checkpoint != want {
		t.Fatalf("must retain intent and fail ADD: err=%v checkpoint=%q", err, m.checkpoint)
	}
	deletes, creates := m.deleteCalls, m.createCalls
	m.completeErr = nil
	_, err = ensureNetworkExistsWithAPI("Calico", mustParseCIDR("10.3.16.0/26"), mustParseCIDR("2001:db8:2::/122"), "", "", testLogger(), m)
	if err != nil || m.route != want || m.checkpoint != "" {
		t.Fatalf("retry lost intent: err=%v route=%q checkpoint=%q", err, m.route, m.checkpoint)
	}
	if m.deleteCalls != deletes || m.createCalls != creates {
		t.Fatal("route recovery unnecessarily recreated a compatible network")
	}
}
