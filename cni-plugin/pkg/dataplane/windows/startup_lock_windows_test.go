package windows

import (
	"errors"
	"sync"
	"testing"
	"time"
)

type observedStartupHNS struct {
	*mockHNS
	entered chan struct{}
	once    sync.Once
}

func (m *observedStartupHNS) GetByName(name string) (*HNSNetworkInfo, error) {
	m.once.Do(func() { close(m.entered) })
	return m.mockHNS.GetByName(name)
}

// Uses the real Windows named mutex but fake HNS, so it cannot modify a
// host's network. Startup must not even observe HNS while CNI owns the lock.
func TestStartupL2BridgeWaitsForCNINetworkLock(t *testing.T) {
	lock, err := acquireLock()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	stop := errors.New("stop before native endpoint operations")
	mock := &observedStartupHNS{mockHNS: newMockHNS(), entered: make(chan struct{})}
	mock.networks["Calico"] = &HNSNetworkInfo{Name: "Calico", Type: "L2Bridge"}
	mock.deleteErr = stop
	previous := defaultHNS
	defaultHNS = mock
	defer func() { defaultHNS = previous }()
	started, done := make(chan struct{}), make(chan error, 1)
	go func() {
		close(started)
		_, err := SetupL2bridgeNetworkAllowRecreate("Calico", mustParseCIDR("10.3.16.0/26"), nil, "", "", testLogger())
		done <- err
	}()
	<-started
	early := false
	select {
	case <-mock.entered:
		early = true
	case <-time.After(250 * time.Millisecond):
	}
	lock.Release()
	select {
	case err := <-done:
		if !errors.Is(err, stop) {
			t.Fatalf("startup did not resume through the expected mock path: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("startup did not resume after CNI released lock")
	}
	if early {
		t.Fatal("startup accessed shared HNS while CNI still held its network lock")
	}
}
