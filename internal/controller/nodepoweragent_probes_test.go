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

package controller

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// F-64: `--version` prints a string and returns before any configuration is parsed. Nothing about
// the process's actual job can make it fail, which is why it passed on an actuator parked forever
// in block().
func TestActuatorReadinessProbeDoesNotRunVersion(t *testing.T) {
	command := actuatorReadinessProbe().Exec.Command

	if strings.Contains(strings.Join(command, " "), "--version") {
		t.Errorf("actuator readiness must not be --version, which cannot fail (F-64): %v", command)
	}
	if len(command) != 2 || command[1] != "--ready" {
		t.Errorf("actuator readiness must exec the binary's own readiness check, got: %v", command)
	}
}

// runProbeScript runs the rendered probe against a upsmon.conf fixture, with a stub `upsc` on PATH
// so the assertion is about the script's control flow rather than about NUT.
func runProbeScript(t *testing.T, upsmonConf string, upscExit int) error {
	t.Helper()

	root := t.TempDir()
	nutDir := filepath.Join(root, "etc", "nut")
	if err := os.MkdirAll(nutDir, 0o755); err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nutDir, "upsmon.conf"), []byte(upsmonConf), 0o600); err != nil {
		t.Fatalf("write upsmon.conf: %v", err)
	}

	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("create bin: %v", err)
	}
	stub := "#!/bin/sh\nexit " + string(rune('0'+upscExit)) + "\n"
	if err := os.WriteFile(filepath.Join(binDir, "upsc"), []byte(stub), 0o755); err != nil {
		t.Fatalf("write upsc stub: %v", err)
	}
	// The probe's third step asks upsmon's own notification record whether it still has a session
	// (F-65). Stubbed to pass so these cases stay about the upsc half; the check itself is covered
	// in cmd/power-notify-writer.
	if err := os.WriteFile(filepath.Join(binDir, "power-notify-writer"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write power-notify-writer stub: %v", err)
	}

	script := upsmonReadinessProbe().Exec.Command[2]
	// The rendered script hard-codes the container's absolute config path; point it at the fixture.
	script = strings.ReplaceAll(script, "/etc/nut/upsmon.conf", filepath.Join(nutDir, "upsmon.conf"))

	command := exec.Command("sh", "-ec", script)
	command.Env = append(os.Environ(), "PATH="+binDir+":"+os.Getenv("PATH"))
	return command.Run()
}

// F-65's headline defect, reproduced against the rendered script rather than described.
//
// The old script piped targets into `while read`, so with no MONITOR lines the loop body never ran
// and the pipeline's exit status was the loop's: zero. A rendering bug that dropped every monitor
// target presented as a healthy agent.
func TestUpsmonReadinessFailsWhenNoMonitorTargetsAreRendered(t *testing.T) {
	err := runProbeScript(t, "RUN_AS_USER nut\nMINSUPPLIES 1\nDEADTIME 45\n", 0)

	if err == nil {
		t.Fatal("a upsmon.conf with zero MONITOR lines must fail readiness, not pass vacuously (F-65)")
	}
}

func TestUpsmonReadinessPassesWhenEveryMonitoredUPSAnswers(t *testing.T) {
	conf := "MONITOR ups-a@server-a.svc 1 monuser monpass secondary\nMONITOR ups-b@server-b.svc 1 monuser monpass secondary\n"

	if err := runProbeScript(t, conf, 0); err != nil {
		t.Fatalf("every monitored UPS answered, so the probe must pass: %v", err)
	}
}

func TestUpsmonReadinessFailsWhenAMonitoredUPSDoesNotAnswer(t *testing.T) {
	conf := "MONITOR ups-a@server-a.svc 1 monuser monpass secondary\n"

	if err := runProbeScript(t, conf, 1); err == nil {
		t.Fatal("a monitored UPS that does not answer must fail readiness")
	}
}

// The old script queried only the host half of the target, an anonymous LIST UPS that passes
// whenever any NUT server is listening -- including when the UPS this agent monitors is not among
// the ones it serves.
func TestUpsmonReadinessQueriesTheFullTargetNotJustTheServer(t *testing.T) {
	script := upsmonReadinessProbe().Exec.Command[2]

	if strings.Contains(script, "upsc -l") {
		t.Error("readiness must query <ups>@<server>, not an anonymous LIST UPS against the host (F-65)")
	}
	if !strings.Contains(script, `upsc "$target"`) {
		t.Errorf("readiness must query the full MONITOR target, got: %s", script)
	}
}
