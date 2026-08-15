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

package resolver

import (
	"fmt"
	"sort"
	"time"
)

// SnapshotAgeLevel raises one severity once a snapshot reaches an age (IN-16).
type SnapshotAgeLevel struct {
	Severity string        `json:"severity"`
	After    time.Duration `json:"after"`
}

// DefaultSnapshotAgeLevels are applied when an install configures none.
//
// The declarative CRD provider rebuilds from live resources and leaves its
// snapshot unstamped, so these never fire for it. They exist for providers that
// can go away mid-outage, where planning deliberately continues against the last
// snapshot obtained (IN-15) and the only remaining protection is saying how old
// that snapshot has become.
func DefaultSnapshotAgeLevels() []SnapshotAgeLevel {
	return []SnapshotAgeLevel{
		{Severity: DiagnosticInfo, After: time.Hour},
		{Severity: DiagnosticWarning, After: 6 * time.Hour},
	}
}

// EvaluateSnapshotAge reports how stale an inventory snapshot has become.
//
// It returns the highest-severity level the snapshot has aged past, or nil when
// it is fresh enough to say nothing about. There is deliberately no level that
// rejects: IN-15 makes provider staleness degrade rather than block, because a
// shutdown that refuses to plan during an outage is worse than one planned from
// an old topology.
//
// A snapshot with no timestamp produces nothing. Silence is correct when there
// is nothing to measure, the same way node checks stay quiet with no nodes
// visible — inventing staleness from a missing field would report a fault in the
// snapshot's metadata as a fault in its age.
func EvaluateSnapshotAge(observedAt string, now time.Time, levels []SnapshotAgeLevel) *Diagnostic {
	if observedAt == "" {
		return nil
	}
	stamped, err := time.Parse(time.RFC3339, observedAt)
	if err != nil {
		return &Diagnostic{
			Severity: DiagnosticWarning,
			Source:   DiagnosticSourceInventory,
			Reason:   "InventorySnapshotTimestampInvalid",
			Message: fmt.Sprintf(
				"inventory snapshot observedAt %q is not an RFC3339 timestamp, so its age cannot be evaluated",
				observedAt),
		}
	}

	age := now.Sub(stamped)
	if age < 0 {
		// A snapshot stamped in the future is clock skew, not staleness. Reporting
		// it as fresh is the honest reading: it has not aged past anything.
		return nil
	}
	if len(levels) == 0 {
		levels = DefaultSnapshotAgeLevels()
	}

	ordered := append([]SnapshotAgeLevel(nil), levels...)
	sort.SliceStable(ordered, func(left, right int) bool {
		return ordered[left].After > ordered[right].After
	})
	for _, level := range ordered {
		if age < level.After {
			continue
		}
		return &Diagnostic{
			Severity: level.Severity,
			Source:   DiagnosticSourceInventory,
			Reason:   "InventorySnapshotAging",
			Message: fmt.Sprintf(
				"inventory snapshot is %s old, past the %s threshold for %s; planning continues against it",
				age.Round(time.Second), level.After, level.Severity),
		}
	}
	return nil
}
