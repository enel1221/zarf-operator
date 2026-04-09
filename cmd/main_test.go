/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/manager"
)

func TestSidecarConnectionMonitorNeedLeaderElection(t *testing.T) {
	m := newSidecarConnectionMonitor("localhost:50051", nil)

	// Verify it implements LeaderElectionRunnable
	ler, ok := interface{}(m).(manager.LeaderElectionRunnable)
	if !ok {
		t.Fatal("sidecarConnectionMonitor must implement manager.LeaderElectionRunnable")
	}

	// The monitor must NOT require leader election so it can connect
	// to the sidecar and pass readiness checks before the lease is acquired.
	if ler.NeedLeaderElection() {
		t.Error("sidecarConnectionMonitor.NeedLeaderElection() must return false; " +
			"otherwise the readiness probe fails while waiting for leader election")
	}
}
