package calico

import (
	"testing"
	"time"
)

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

func TestOnSyncChangeUnblocksWithoutLocalBGPPeerWatcher(t *testing.T) {
	c := &client{
		sourceReady: map[string]bool{},
	}
	c.waitForSync.Add(1)

	c.OnSyncChange(SourceRouteGenerator, true)
	c.OnSyncChange(SourceSyncer, true)

	done := make(chan struct{})
	go func() {
		c.waitForSync.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("expected full sync to unblock rendering without local BGP peer watcher")
	}

	if !c.syncedOnce {
		t.Fatal("expected sync transition to mark the client as synced")
	}
}
