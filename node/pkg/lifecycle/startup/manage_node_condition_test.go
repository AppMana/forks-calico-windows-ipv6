// Copyright (c) 2026 Tigera, Inc. All rights reserved.

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

package startup

import (
	"context"
	"os"
	"testing"
	"time"
)

func newCancellableContext() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}

// TestManageNodeConditionLifecycle is a plain go test (outside the ginkgo
// suite, which requires a live datastore) covering the complete-startup
// manager loop. Regression: the previous implementation blocked on
// done.Done() — a nil channel for context.Background() — which left the
// lone goroutine asleep and crashed calico-node.exe -complete-startup with
// "fatal error: all goroutines are asleep - deadlock!" right after marking
// the node available.
func TestManageNodeConditionLifecycle(t *testing.T) {
	// "none" makes MarkNetworkAvailable a no-op so no apiserver is needed.
	os.Setenv("CALICO_NETWORKING_BACKEND", "none")
	defer os.Unsetenv("CALICO_NETWORKING_BACKEND")

	oldInterval := nodeConditionReassertInterval
	nodeConditionReassertInterval = 10 * time.Millisecond
	defer func() { nodeConditionReassertInterval = oldInterval }()

	ctx, cancel := newCancellableContext()
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- ManageNodeCondition(ctx, 100*time.Millisecond) }()

	// Let several re-assert ticks fire; the manager must keep running.
	time.Sleep(150 * time.Millisecond)
	select {
	case err := <-result:
		t.Fatalf("ManageNodeCondition returned before cancellation: %v", err)
	default:
	}

	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("ManageNodeCondition returned error on cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ManageNodeCondition did not return after context cancellation")
	}
}
