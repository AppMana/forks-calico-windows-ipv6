package windows

import (
	"errors"
	"testing"

	"github.com/sirupsen/logrus"
)

// The real Windows/VyOS lab loses the old RRAS TCP connection when HNS
// removes the management interface. BIRD then rejects replacement connections
// until the old session's hold timer expires. Model the ordering requirement,
// not TCP timers: a planned interface removal must first close its BGP session.
// Native VM packet capture must separately verify that the Windows operation
// actually sends the close while the old interface is still available.
type bgpTransitionHNS struct {
	*mockHNS
	connected, staleRemoteSession bool
	beginErr, completeErr         error
	begins, completes             int
}

func (m *bgpTransitionHNS) BeginBGPSessionTransition(*logrus.Entry) error {
	m.begins++
	if m.beginErr != nil {
		return m.beginErr
	}
	m.connected = false
	return nil
}

func (m *bgpTransitionHNS) CompleteBGPSessionTransition(*logrus.Entry) error {
	m.completes++
	if m.completeErr != nil {
		return m.completeErr
	}
	m.connected = true
	return nil
}

func (m *bgpTransitionHNS) Delete(n *HNSNetworkInfo) error {
	if m.connected {
		m.staleRemoteSession = true
	}
	return m.mockHNS.Delete(n)
}

func newBGPTransitionHNS() *bgpTransitionHNS {
	m := &bgpTransitionHNS{mockHNS: newMockHNS(), connected: true}
	m.networks["Calico"] = &HNSNetworkInfo{Name: "Calico", Type: "L2Bridge", Subnets: []HNSSubnet{
		{AddressPrefix: "10.3.16.0/26", GatewayAddress: "10.3.16.1"},
		{AddressPrefix: "2001:db8:1::/122", GatewayAddress: "2001:db8:1::1"},
	}}
	return m
}

func TestPrefixReplacementClosesBGPBeforeRemovingInterface(t *testing.T) {
	m := newBGPTransitionHNS()
	_, err := ensureNetworkExistsWithAPI("Calico", mustParseCIDR("10.3.16.0/26"), mustParseCIDR("2001:db8:2::/122"), "", "", testLogger(), m)
	if err != nil {
		t.Fatal(err)
	}
	if m.deleteCalls == 0 || m.createCalls == 0 {
		t.Fatal("test did not replace the HNS network")
	}
	if m.staleRemoteSession || m.begins != 1 || m.completes != 1 || !m.connected {
		t.Fatalf("planned rebind stranded BGP: stale=%v begin=%d complete=%d connected=%v", m.staleRemoteSession, m.begins, m.completes, m.connected)
	}
}

func TestPrefixReplacementDoesNotDeleteWhenBGPCloseFails(t *testing.T) {
	m := newBGPTransitionHNS()
	m.beginErr = errors.New("BGP close failed")
	_, err := ensureNetworkExistsWithAPI("Calico", mustParseCIDR("10.3.16.0/26"), mustParseCIDR("2001:db8:2::/122"), "", "", testLogger(), m)
	if err == nil || m.deleteCalls != 0 || m.createCalls != 0 {
		t.Fatalf("must fail before HNS mutation: err=%v deletes=%d creates=%d", err, m.deleteCalls, m.createCalls)
	}
}

func TestCompatibleNetworkDoesNotInterruptBGP(t *testing.T) {
	m := newBGPTransitionHNS()
	_, err := ensureNetworkExistsWithAPI("Calico", mustParseCIDR("10.3.16.0/26"), mustParseCIDR("2001:db8:1::/122"), "", "", testLogger(), m)
	if err != nil || m.begins != 0 || m.staleRemoteSession || m.deleteCalls != 0 {
		t.Fatalf("ordinary ADD must preserve BGP: err=%v begin=%d stale=%v deletes=%d", err, m.begins, m.staleRemoteSession, m.deleteCalls)
	}
}

func TestPrefixReplacementRetriesBGPRestartWithoutDeletingAgain(t *testing.T) {
	m := newBGPTransitionHNS()
	m.completeErr = errors.New("BGP restart failed")
	_, err := ensureNetworkExistsWithAPI("Calico", mustParseCIDR("10.3.16.0/26"), mustParseCIDR("2001:db8:2::/122"), "", "", testLogger(), m)
	if err == nil || m.connected || m.staleRemoteSession {
		t.Fatalf("failed restart must fail ADD without losing graceful close: err=%v connected=%v stale=%v", err, m.connected, m.staleRemoteSession)
	}
	deletes, creates := m.deleteCalls, m.createCalls
	m.completeErr = nil
	_, err = ensureNetworkExistsWithAPI("Calico", mustParseCIDR("10.3.16.0/26"), mustParseCIDR("2001:db8:2::/122"), "", "", testLogger(), m)
	if err != nil || !m.connected || m.deleteCalls != deletes || m.createCalls != creates || m.begins != 1 {
		t.Fatalf("retry must resume without another rebind: err=%v connected=%v deletes=%d creates=%d begin=%d", err, m.connected, m.deleteCalls, m.createCalls, m.begins)
	}
}
