/*
Copyright 2026 Michael Zalud.

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

package nutsupervisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// F-110: TestF124UnrelatedDriverKeepsItsPIDOnUPSConfChange (nutserver_driver_supervisor_component_test.go)
// proves the per-device digest fix once -- add one device, check the unrelated PID survived. A
// bookkeeping bug in per-device digest/pid-file tracking is exactly the kind of thing a single
// pass would not show: a stray file left behind on removal, or a digest write racing the next
// reconcile pass, only accumulates or reproduces across many repeated cycles. This test runs the
// add/remove cycle many times in one supervisor lifetime rather than once.
func TestDriverSupervisorPIDStabilitySurvivesManyRepeatedAddRemoveCycles(t *testing.T) {
	const cycles = 15

	h := newDriverSupervisorHarness(t)
	h.setBehavior("stable", "run")
	h.setConfiguredUPS("stable")
	h.start()

	var stablePID int
	eventually(t, 5*time.Second, func() bool {
		pid, ok := h.pid("stable")
		if !ok || !alive(pid) {
			return false
		}
		stablePID = pid
		return true
	})

	for cycle := 0; cycle < cycles; cycle++ {
		h.setBehavior("churn", "run")
		h.setConfiguredUPS("stable", "churn")

		eventually(t, 5*time.Second, func() bool {
			pid, ok := h.pid("churn")
			return ok && alive(pid)
		})
		if pid, ok := h.pid("stable"); !ok || pid != stablePID || !alive(pid) {
			t.Fatalf("cycle %d: stable driver's PID changed or died on add (was %d, now pid=%d ok=%v)",
				cycle, stablePID, pid, ok)
		}

		h.setConfiguredUPS("stable")

		// churn's pid and digest files must both be reclaimed, not just its process stopped --
		// reconcileDrivers removes them explicitly on an undesired-driver pass (nutserver_render.go),
		// and a bug there would leave one more stray file behind every cycle rather than failing
		// any single cycle outright, which is why this checks it every time rather than once at
		// the end.
		eventually(t, 5*time.Second, func() bool {
			_, pidErr := os.Stat(filepath.Join(h.stateDir, "churn.pid"))
			_, digestErr := os.Stat(filepath.Join(h.stateDir, "churn.digest"))
			return os.IsNotExist(pidErr) && os.IsNotExist(digestErr)
		})
		if pid, ok := h.pid("stable"); !ok || pid != stablePID || !alive(pid) {
			t.Fatalf("cycle %d: stable driver's PID changed or died on remove (was %d, now pid=%d ok=%v)",
				cycle, stablePID, pid, ok)
		}
	}

	// The property a single cycle cannot show: no orphaned per-device state accumulated in the
	// state directory across churn's fifteen add/remove passes. "churn" and "stable" account for
	// every managed file; anything else is a leak.
	entries, err := os.ReadDir(h.stateDir)
	if err != nil {
		t.Fatalf("read state dir: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		// list.err is configuredDrivers' stderr capture (nutserver_render.go) -- shell
		// redirection creates it on every listing pass regardless of outcome, so its presence is
		// expected bookkeeping, not a leak.
		if name == "list.err" {
			continue
		}
		if !strings.HasPrefix(name, "stable.") {
			t.Fatalf("state directory retained an unexpected entry after %d add/remove cycles: %s (full listing: %v)",
				cycles, name, direntNames(entries))
		}
	}
}

func direntNames(entries []os.DirEntry) []string {
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}
