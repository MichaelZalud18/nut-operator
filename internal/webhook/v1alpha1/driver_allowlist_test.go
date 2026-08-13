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

package v1alpha1

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The admission allowlist and the operand image are two independent lists of drivers, and
// F-50 is what happens when they disagree: powerman-pdu stayed admissible across the move to a
// source build that stopped compiling it, so a UPSDevice naming it passed validation, rendered
// into ups.conf, and could never start.
//
// Neither half can catch that alone. The smoke test can see the image but not the Go allowlist;
// this test can see the Go allowlist but not the image. Pinning them to each other is what makes
// the pair a guard: a driver added here without being added to the smoke test fails now, and a
// driver in both that the image lacks fails there.
var smokeTestDriverListPattern = regexp.MustCompile(`ALLOWLISTED_DRIVERS="([^"]*)"`)

const smokeImageScriptPath = "../../../hack/smoke-image.sh"

func TestSmokeTestCoversEveryAllowlistedDriver(t *testing.T) {
	script, err := os.ReadFile(smokeImageScriptPath)
	if err != nil {
		t.Fatalf("read smoke image script: %v", err)
	}
	match := smokeTestDriverListPattern.FindSubmatch(script)
	if match == nil {
		t.Fatalf("no ALLOWLISTED_DRIVERS assignment found in %s", smokeImageScriptPath)
	}

	smokeDrivers := strings.Fields(string(match[1]))
	if len(smokeDrivers) == 0 {
		t.Fatal("ALLOWLISTED_DRIVERS is empty, so the smoke test asserts nothing")
	}

	admitted := append([]string(nil), supportedNetworkUPSDrivers()...)
	sort.Strings(admitted)
	sort.Strings(smokeDrivers)

	if strings.Join(admitted, " ") != strings.Join(smokeDrivers, " ") {
		t.Errorf("admission allowlist and smoke test driver list disagree:\n  admission: %v\n  smoke:     %v",
			admitted, smokeDrivers)
	}
}

// powerman-pdu gets its own assertion rather than resting on the comparison above, because the
// comparison passes just as happily if someone re-adds it to both lists. Alpine does not package
// libpowerman, so building the driver in means a second source build inside every operand image;
// until that trade is deliberately made, admitting the driver is admitting a device that cannot
// start.
func TestPowermanPDUIsNotAdmitted(t *testing.T) {
	if isSupportedNetworkUPSDriver("powerman-pdu") {
		t.Error("powerman-pdu is admitted but images/nut-server/Dockerfile builds --without-powerman (F-50)")
	}
}

// OD-36 declines clone, clone-outlet, and failover. Unlike powerman-pdu they are present in the
// operand image -- they build unconditionally as part of NUTSW_DRIVERLIST and there is no
// configure flag to exclude them -- so the decision is enforced by the allowlist alone, and this
// is where it is enforced.
//
// clone and clone-outlet are staged-shutdown sequencers: a virtual UPS presenting earlier
// thresholds than the device it shadows. SB-2b reserves sequencing for the operator, and admitting
// one would put a second sequencer in the cluster with no view of it -- the objection that
// declined upssched in F-21. The closer analogy makes it more dangerous, not less: clone looks
// like it would work.
//
// failover presents several physical devices as one, which resembles the multi-supply-per-host
// topology F-45 records as inexpressible. That gap is in the render and the inventory model, not
// the driver layer, so the driver would not close it.
func TestSequencingDriversAreNotAdmitted(t *testing.T) {
	for _, driver := range []string{"clone", "clone-outlet", "failover"} {
		if isSupportedNetworkUPSDriver(driver) {
			t.Errorf("%s is declined by OD-36 but the allowlist admits it", driver)
		}
	}
}
