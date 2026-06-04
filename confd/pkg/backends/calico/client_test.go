package calico

import "testing"

func TestInSyncTreatsMissingLocalBGPPeerWatcherAsReady(t *testing.T) {
	c := &client{
		sourceReady: map[string]bool{
			SourceSyncer:         true,
			SourceRouteGenerator: true,
		},
	}

	if !c.inSync() {
		t.Fatal("expected full sync when syncer and route generator are ready and local BGP peer watcher is not running")
	}
}

func TestInSyncRequiresLocalBGPPeerWatcherWhenRunning(t *testing.T) {
	c := &client{
		localBGPPeerWatcher: &LocalBGPPeerWatcher{},
		sourceReady: map[string]bool{
			SourceSyncer:         true,
			SourceRouteGenerator: true,
		},
	}

	if c.inSync() {
		t.Fatal("expected local BGP peer watcher readiness to be required when the watcher is running")
	}

	c.sourceReady[SourceLocalBGPPeerWatcher] = true
	if !c.inSync() {
		t.Fatal("expected full sync once the running local BGP peer watcher is ready")
	}
}
