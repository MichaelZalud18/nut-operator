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

package executor

import (
	"testing"
	"time"
)

// tierWindowOverrunning answers, while a tier is still running, the question tierOverrunRecord
// answers afterwards. The distinction matters: the record exists for the audit trail, and by the
// time it is written the actions that could have been shortened have already run.
func TestTierWindowOverrunning(t *testing.T) {
	start := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)

	for _, testCase := range []struct {
		name   string
		window tierOverrunWindow
		now    time.Time
		want   bool
	}{
		{
			name:   "inside the budget",
			window: tierOverrunWindow{StartedAt: start, EffectiveDuration: 5 * time.Minute},
			now:    start.Add(4 * time.Minute),
			want:   false,
		},
		{
			name:   "past the budget",
			window: tierOverrunWindow{StartedAt: start, EffectiveDuration: 5 * time.Minute},
			now:    start.Add(6 * time.Minute),
			want:   true,
		},
		{
			// Exactly at the budget is not yet over it. A tier that finishes on its last second
			// finished on time, and rounding that to "late" would drop the flush on plans that
			// were never actually late.
			name:   "exactly at the budget",
			window: tierOverrunWindow{StartedAt: start, EffectiveDuration: 5 * time.Minute},
			now:    start.Add(5 * time.Minute),
			want:   false,
		},
		{
			// An untimed tier is not a late one. Treating a missing budget as an exceeded budget
			// would make every untiered flow skip its sync, which is the opposite of the intent.
			name:   "no effective duration",
			window: tierOverrunWindow{StartedAt: start},
			now:    start.Add(time.Hour),
			want:   false,
		},
		{
			name:   "never started",
			window: tierOverrunWindow{EffectiveDuration: 5 * time.Minute},
			now:    start.Add(time.Hour),
			want:   false,
		},
		{
			name:   "zero window",
			window: tierOverrunWindow{},
			now:    start,
			want:   false,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := tierWindowOverrunning(testCase.window, testCase.now); got != testCase.want {
				t.Errorf("tierWindowOverrunning = %v, want %v", got, testCase.want)
			}
		})
	}
}
