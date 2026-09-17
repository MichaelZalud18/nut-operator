//go:build linux

package nutsupervisor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestSmokeHarnessCancellationCleansOwnedContainer(t *testing.T) {
	dir := t.TempDir()
	tool := filepath.Join(dir, "container-tool")
	stub := "#!/bin/sh\ncase \"$1\" in\nrun) touch \"$FIXTURE/started\"; exec sleep 30;;\nrm) touch \"$FIXTURE/removed\";;\nesac\n"
	if err := os.WriteFile(tool, []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "../../hack/nut-supervisor-smoke.sh", tool, "unused-image")
	cmd.Env = append(os.Environ(), "FIXTURE="+dir, "NUT_READINESS_SAMPLES=0", "TMPDIR="+dir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	eventually(t, time.Second, func() bool {
		_, err := os.Stat(filepath.Join(dir, "started"))
		return err == nil
	})
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	err := cmd.Wait()
	if ctx.Err() != nil {
		t.Fatal("cancellation waited for the container command instead of running cleanup")
	}
	if err == nil {
		t.Fatal("cancellation unexpectedly reported success")
	}
	if _, err := os.Stat(filepath.Join(dir, "removed")); err != nil {
		t.Fatalf("owned container was not removed: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "tmp.*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary identity leaked: %v, %v", matches, err)
	}
}

// These subprocess fixtures exercise ownership and traps without a Docker daemon.
// The protocol harness compiler is stubbed too; protocol behavior has its own tests.
func newBoundedSmokeFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	tool := filepath.Join(dir, "container-tool")
	stub := `#!/usr/bin/env bash
set -eu
case "$1" in
  image)
    if [[ "${3:-}" == --format ]]; then echo sha256:fixture-image;
    else echo '[{"Id":"sha256:fixture-image"}]'; fi
    ;;
  create)
    shift
    while (( $# )); do
      case "$1" in
        --name) printf '%s\n' "$2" > "$FIXTURE/owned-name"; shift ;;
        --mount)
          case "$2" in
            type=bind,src=*,dst=/tmp/observation)
              output="${2#type=bind,src=}"
              output="${output%,dst=/tmp/observation}"
              printf 'fixture\tretained\n' > "$output/summary.tsv"
              ;;
          esac
          shift
          ;;
      esac
      shift
    done
    if [[ "${SMOKE_CREATE_MODE:-normal}" == hang ]]; then exec sleep 30; fi
    if [[ "${SMOKE_CREATE_EXIT:-0}" != 0 ]]; then exit "$SMOKE_CREATE_EXIT"; fi
    echo fixture-owned-id > "$FIXTURE/owned-id"
    touch "$FIXTURE/present"
    echo fixture-owned-id
    ;;
  start)
    [[ "${@: -1}" == "$(cat "$FIXTURE/owned-id")" ]] || exit 65
    touch "$FIXTURE/started"
    if [[ "${SMOKE_RUN_MODE:-success}" == wait ]]; then exec sleep 30; fi
    if [[ "${SMOKE_REMOVE_MODE:-normal}" == auto ]]; then rm "$FIXTURE/present"; fi
    exit "${SMOKE_RUN_EXIT:-0}"
    ;;
  stop) printf '%s\n' "${@: -1}" > "$FIXTURE/stopped-name" ;;
  logs) echo 'fixture container logs' ;;
  rm)
    printf '%s\n' "${@: -1}" > "$FIXTURE/removed-name"
    case "${SMOKE_REMOVE_MODE:-normal}" in
      hang) exec sleep 30 ;;
      left) exit 0 ;;
      auto) exit 1 ;;
    esac
    rm -f "$FIXTURE/present"
    ;;
  ps)
    printf '%s\n' "$@" > "$FIXTURE/absence-query"
    [[ "${SMOKE_PS_FAIL:-0}" == 0 ]] || exit 1
    if [[ -e "$FIXTURE/present" ]]; then cat "$FIXTURE/owned-name"; fi
    ;;
  *) exit 64 ;;
esac
`
	if err := os.WriteFile(tool, []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	compiler := `#!/bin/sh
set -eu
while [ "$#" -gt 0 ]; do
  if [ "$1" = -o ]; then
    printf '#!/bin/sh\necho TestRealNUTFixture\n' > "$2"
    chmod 0755 "$2"
    exit 0
  fi
  shift
done
exit 64
`
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte(compiler), 0o700); err != nil {
		t.Fatal(err)
	}
	return dir, tool
}

func runBoundedSmokeFixture(t *testing.T, script, dir, tool string, cancelRun bool, wantExit int, extraEnv ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", filepath.Join("../../hack", script), tool, "fixture-image")
	cmd.Env = append(os.Environ(), "FIXTURE="+dir, "TMPDIR="+dir,
		"NUT_STARTUP_ARTIFACT_ROOT="+dir, "NUT_STARTUP_SECONDS=20",
		"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd.Env = append(cmd.Env, extraEnv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	outputPath := filepath.Join(dir, "harness.log")
	output, err := os.Create(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = output.Close() }()
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	if cancelRun {
		eventually(t, 2*time.Second, func() bool {
			_, err := os.Stat(filepath.Join(dir, "started"))
			return err == nil
		})
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
	}
	err = cmd.Wait()
	if ctx.Err() != nil {
		t.Fatalf("%s exceeded the fixture deadline", script)
	}
	gotExit := 0
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		gotExit = exitErr.ExitCode()
	}
	if gotExit != wantExit {
		log, _ := os.ReadFile(outputPath)
		t.Fatalf("%s exit=%d, want %d: %s", script, gotExit, wantExit, log)
	}
}

func assertSmokeOwnedRemoval(t *testing.T, dir string) {
	t.Helper()
	owned, err := os.ReadFile(filepath.Join(dir, "owned-id"))
	if err != nil || len(owned) == 0 {
		t.Fatalf("missing owned container identity: %v", err)
	}
	removed, err := os.ReadFile(filepath.Join(dir, "removed-name"))
	if err != nil || string(removed) != string(owned) {
		t.Fatalf("cleanup target %q differs from owned container %q: %v", removed, owned, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "present")); !os.IsNotExist(err) {
		t.Fatalf("owned container remains: %v", err)
	}
}

func assertSmokeArtifactLine(t *testing.T, path, line string) {
	t.Helper()
	if output, err := exec.Command("grep", "-Fx", "--", line, path).CombinedOutput(); err != nil {
		t.Fatalf("%s lacks %q: %v, %s", path, line, err, output)
	}
}

func TestNewSmokeHarnessesRejectMissingArguments(t *testing.T) {
	for _, script := range []string{"nut-startup-smoke.sh", "nut-driver-ready-smoke.sh", "nut-readiness-protocol-smoke.sh"} {
		t.Run(script, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := exec.CommandContext(ctx, "bash", filepath.Join("../../hack", script)).Run()
			exitErr, ok := err.(*exec.ExitError)
			if !ok || exitErr.ExitCode() != 64 {
				t.Fatalf("expected usage exit 64, got %v", err)
			}
		})
	}
}

func TestStartupSmokeRejectsInvalidDurationBeforeContainerWork(t *testing.T) {
	for _, duration := range []string{"0", "661", "-1", "1.5", "001", "not-seconds"} {
		t.Run(duration, func(t *testing.T) {
			dir, tool := newBoundedSmokeFixture(t)
			runBoundedSmokeFixture(t, "nut-startup-smoke.sh", dir, tool, false, 64, "NUT_STARTUP_SECONDS="+duration)
			matches, err := filepath.Glob(filepath.Join(dir, "nut-startup.*"))
			if err != nil || len(matches) != 0 {
				t.Fatalf("invalid duration created artifacts: %v, %v", matches, err)
			}
			if _, err := os.Stat(filepath.Join(dir, "started")); !os.IsNotExist(err) {
				t.Fatalf("invalid duration started a container: %v", err)
			}
		})
	}
}

func TestNewSmokeHarnessCancellationRemovesOnlyOwnedContainer(t *testing.T) {
	for _, script := range []string{"nut-startup-smoke.sh", "nut-driver-ready-smoke.sh", "nut-readiness-protocol-smoke.sh"} {
		t.Run(script, func(t *testing.T) {
			dir, tool := newBoundedSmokeFixture(t)
			runBoundedSmokeFixture(t, script, dir, tool, true, 143, "SMOKE_RUN_MODE=wait")
			assertSmokeOwnedRemoval(t, dir)
			if script == "nut-startup-smoke.sh" {
				matches, err := filepath.Glob(filepath.Join(dir, "nut-startup.*", "run.tsv"))
				if err != nil || len(matches) != 1 {
					t.Fatalf("cancellation lost run.tsv: %v, %v", matches, err)
				}
				assertSmokeArtifactLine(t, matches[0], "exit_code\t143")
				assertSmokeArtifactLine(t, matches[0], "container_removed\t1")
				assertSmokeArtifactLine(t, matches[0], "artifacts_collected\t1")
				assertSmokeArtifactLine(t, matches[0], "image_id\tsha256:fixture-image")
				assertSmokeArtifactLine(t, filepath.Join(filepath.Dir(matches[0]), "observation", "summary.tsv"), "fixture\tretained")
			} else {
				matches, err := filepath.Glob(filepath.Join(dir, "tmp.*"))
				if err != nil || len(matches) != 0 {
					t.Fatalf("temporary identity leaked: %v, %v", matches, err)
				}
			}
		})
	}
}

func TestReadySmokeRequiresConfirmedContainerAbsence(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  string
		exit int
	}{
		{"normal removal", "SMOKE_REMOVE_MODE=normal", 0},
		{"auto removal before cleanup", "SMOKE_REMOVE_MODE=auto", 0},
		{"rm succeeds but container remains", "SMOKE_REMOVE_MODE=left", 1},
		{"daemon query fails", "SMOKE_PS_FAIL=1", 1},
		{"preserve original run failure", "SMOKE_RUN_EXIT=17", 17},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, tool := newBoundedSmokeFixture(t)
			runBoundedSmokeFixture(t, "nut-driver-ready-smoke.sh", dir, tool, false, tc.exit, tc.env)
			if _, err := os.Stat(filepath.Join(dir, "absence-query")); err != nil {
				t.Fatalf("no daemon query confirmed removal: %v", err)
			}
			if tc.exit == 0 {
				assertSmokeOwnedRemoval(t, dir)
			}
		})
	}
}

func TestReadySmokeBoundsHungContainerRemoval(t *testing.T) {
	dir, tool := newBoundedSmokeFixture(t)
	start := time.Now()
	runBoundedSmokeFixture(t, "nut-driver-ready-smoke.sh", dir, tool, false, 1, "SMOKE_REMOVE_MODE=hang")
	if elapsed := time.Since(start); elapsed < 9*time.Second || elapsed > 15*time.Second {
		t.Fatalf("removal did not respect the ten-second bound: %s", elapsed)
	}
}

func TestReadySmokeBoundsHungAbsenceQuery(t *testing.T) {
	dir, tool := newBoundedSmokeFixture(t)
	wrapper := filepath.Join(dir, "slow-query-tool")
	stub := "#!/bin/sh\nif [ \"$1\" = ps ]; then exec sleep 30; fi\nexec \"$REAL_CONTAINER_TOOL\" \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	runBoundedSmokeFixture(t, "nut-driver-ready-smoke.sh", dir, wrapper, false, 1, "REAL_CONTAINER_TOOL="+tool)
	if elapsed := time.Since(start); elapsed < 4*time.Second || elapsed > 10*time.Second {
		t.Fatalf("absence query did not respect the five-second bound: %s", elapsed)
	}
	assertSmokeOwnedRemoval(t, dir)
}

func TestSmokeCancellationBeforeCreationSettlesOwnedID(t *testing.T) {
	for _, script := range []string{"nut-driver-ready-smoke.sh", "nut-startup-smoke.sh", "nut-readiness-protocol-smoke.sh"} {
		t.Run(script, func(t *testing.T) {
			dir, tool := newBoundedSmokeFixture(t)
			stub := `#!/usr/bin/env bash
set -eu
case "$1" in
  image)
    if [[ "${3:-}" == --format ]]; then echo sha256:fixture-image;
    else echo '[{"Id":"sha256:fixture-image"}]'; fi
    ;;
  create|run)
    operation=$1
    cidfile=
    shift
    while (( $# )); do
      case "$1" in
        --name) printf '%s\n' "$2" > "$FIXTURE/owned-name"; shift ;;
        --cidfile) cidfile=$2; shift ;;
      esac
      shift
    done
    # Stand in for a daemon completing an already-accepted create request even
    # if its client is signaled. The harness must settle creation before removal.
    trap '' TERM
    touch "$FIXTURE/creation-entered"
    sleep 0.3
    echo fixture-owned-id > "$FIXTURE/present"
    [[ -z "$cidfile" ]] || echo fixture-owned-id > "$cidfile"
    touch "$FIXTURE/creation-completed"
    echo fixture-owned-id
    if [[ "$operation" == run ]]; then touch "$FIXTURE/workload-started"; exec sleep 30; fi
    ;;
  start) touch "$FIXTURE/workload-started"; exec sleep 30 ;;
  stop|logs) : ;;
  rm)
    printf '%s\n' "${@: -1}" > "$FIXTURE/removed-id"
    if [[ ! -e "$FIXTURE/creation-completed" ]]; then touch "$FIXTURE/premature-removal"; fi
    rm -f "$FIXTURE/present"
    ;;
  ps) if [[ -e "$FIXTURE/present" ]]; then cat "$FIXTURE/present"; fi ;;
  inspect) if [[ -e "$FIXTURE/present" ]]; then cat "$FIXTURE/present"; else exit 1; fi ;;
  *) exit 64 ;;
esac
`
			if err := os.WriteFile(tool, []byte(stub), 0o700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", filepath.Join("../../hack", script), tool, "fixture-image")
			cmd.Env = append(os.Environ(), "FIXTURE="+dir, "TMPDIR="+dir, "NUT_STARTUP_ARTIFACT_ROOT="+dir,
				"NUT_STARTUP_SECONDS=20", "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			output, err := os.Create(filepath.Join(dir, "race.log"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = output.Close() }()
			cmd.Stdout, cmd.Stderr = output, output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
			eventually(t, time.Second, func() bool {
				_, err := os.Stat(filepath.Join(dir, "creation-entered"))
				return err == nil
			})
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			err = cmd.Wait()
			if ctx.Err() != nil {
				t.Fatal("cancellation did not settle creation within the bound")
			}
			if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 143 {
				t.Fatalf("cancellation exit must remain 143: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, "premature-removal")); !os.IsNotExist(err) {
				t.Error("cleanup ran before the accepted creation request settled")
			}
			if _, err := os.Stat(filepath.Join(dir, "creation-completed")); err != nil {
				t.Errorf("creation was not joined: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, "workload-started")); !os.IsNotExist(err) {
				t.Error("canceled creation still launched the workload")
			}
			removed, err := os.ReadFile(filepath.Join(dir, "removed-id"))
			if err != nil || string(removed) != "fixture-owned-id\n" {
				t.Errorf("cleanup did not target the created immutable ID: %q, %v", removed, err)
			}
			if _, err := os.Stat(filepath.Join(dir, "present")); !os.IsNotExist(err) {
				t.Error("late creation left an owned container behind")
			}
		})
	}
}

func TestStartupCleanupEvidenceFailureStillReapsChildren(t *testing.T) {
	for _, fault := range []string{"missing-events", "unwritable-artifact", "unwritable-summary"} {
		t.Run(fault, func(t *testing.T) {
			dir := t.TempDir()
			source, err := filepath.Abs("../../hack/nut-startup-smoke-container.sh")
			if err != nil {
				t.Fatal(err)
			}
			child := `#!/bin/sh
trap 'sleep 0.05; touch "$FIXTURE/$1.terminated"; exit 0' TERM
touch "$FIXTURE/$1.started"
while :; do sleep 0.05; done
`
			if err := os.WriteFile(filepath.Join(dir, "child.sh"), []byte(child), 0o700); err != nil {
				t.Fatal(err)
			}
			// Load the actual cleanup and initialization, but never execute the NUT
			// launch section. Redirect its two fixed paths into this test's temp dir.
			fixture := `#!/bin/sh
set -eu
sed -e 's|^output=/tmp/observation$|output="$FIXTURE/observation"|' \
    -e 's|^export NUT_CONFPATH=/tmp/nut-startup$|export NUT_CONFPATH="$FIXTURE/nut"|' \
    -e '/^command -v nut-driver-ready/,$d' "$STARTUP_SOURCE" > "$FIXTURE/cleanup-prefix.sh"
. "$FIXTURE/cleanup-prefix.sh"
sh "$FIXTURE/child.sh" server &
server_pid=$!
sh "$FIXTURE/child.sh" supervisor &
supervisor_pid=$!
sh "$FIXTURE/child.sh" monitor &
monitor_pids=$!
for child in server supervisor monitor; do
  while [ ! -e "$FIXTURE/$child.started" ]; do sleep 0.01; done
done
printf 'User observer1@127.0.0.1 logged into UPS [good]\n' > "$output/upsd.log"
if [ "$FAULT" != missing-events ]; then
  : > "$output/client-1-events.tsv"
fi
if [ "$FAULT" = unwritable-artifact ]; then
  # A directory at the output filename makes redirection fail even as root.
  mkdir "$output/clients.tsv"
fi
if [ "$FAULT" = unwritable-summary ]; then mkdir "$output/summary.tsv"; fi
exit 0
`
			path := filepath.Join(dir, "cleanup-test.sh")
			if err := os.WriteFile(path, []byte(fixture), 0o700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "sh", path)
			cmd.Env = append(os.Environ(), "FIXTURE="+dir, "STARTUP_SOURCE="+source, "FAULT="+fault, "NUT_STARTUP_SECONDS=20")
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			output, err := os.Create(filepath.Join(dir, "cleanup.log"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = output.Close() }()
			cmd.Stdout, cmd.Stderr = output, output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
			if err := cmd.Wait(); err == nil || ctx.Err() != nil {
				t.Fatalf("evidence failure must finish with an error, not hang or pass: %v", err)
			}
			for _, child := range []string{"server", "supervisor", "monitor"} {
				if _, err := os.Stat(filepath.Join(dir, child+".terminated")); err != nil {
					t.Errorf("evidence failure skipped %s cleanup: %v", child, err)
				}
			}
			check := exec.Command("awk", "-F\t", `$1 == "cleanup_evidence_errors" && $2 > 0 { found=1 } END { exit !found }`, filepath.Join(dir, "observation", "summary.tsv"))
			if fault == "unwritable-summary" {
				check = exec.Command("grep", "-F", "startup cleanup evidence failure:", filepath.Join(dir, "cleanup.log"))
			}
			if output, err := check.CombinedOutput(); err != nil {
				t.Errorf("evidence failure was not recorded in the summary: %v, %s", err, output)
			}
		})
	}
}

func TestSmokeCreateFailureIsUncertainEvenWhenAbsent(t *testing.T) {
	for _, script := range []string{"nut-driver-ready-smoke.sh", "nut-startup-smoke.sh", "nut-readiness-protocol-smoke.sh"} {
		t.Run(script, func(t *testing.T) {
			for _, code := range []string{"1", "124"} {
				t.Run(code, func(t *testing.T) {
					dir, tool := newBoundedSmokeFixture(t)
					want := 1
					if code == "124" {
						want = 124
					}
					runBoundedSmokeFixture(t, script, dir, tool, false, want, "SMOKE_CREATE_EXIT="+code)
					if _, err := os.Stat(filepath.Join(dir, "started")); !os.IsNotExist(err) {
						t.Fatal("failed creation was followed by start")
					}
					if output, err := exec.Command("grep", "-F", "cleanup remains uncertain", filepath.Join(dir, "harness.log")).CombinedOutput(); err != nil {
						t.Fatalf("uncertain creation was not reported: %v, %s", err, output)
					}
					if script == "nut-startup-smoke.sh" {
						matches, err := filepath.Glob(filepath.Join(dir, "nut-startup.*", "run.tsv"))
						if err != nil || len(matches) != 1 {
							t.Fatalf("creation failure lost run.tsv: %v, %v", matches, err)
						}
						assertSmokeArtifactLine(t, matches[0], "creation_uncertain\t1")
						assertSmokeArtifactLine(t, matches[0], "container_removed\t0")
					}
				})
			}
		})
	}
}

func TestSmokeCreateTimeoutIsBounded(t *testing.T) {
	for _, script := range []string{"nut-driver-ready-smoke.sh", "nut-startup-smoke.sh", "nut-readiness-protocol-smoke.sh"} {
		t.Run(script, func(t *testing.T) {
			t.Parallel()
			dir, tool := newBoundedSmokeFixture(t)
			start := time.Now()
			runBoundedSmokeFixture(t, script, dir, tool, false, 124, "SMOKE_CREATE_MODE=hang")
			if elapsed := time.Since(start); elapsed < 9*time.Second || elapsed > 15*time.Second {
				t.Fatalf("create did not respect the ten-second bound: %s", elapsed)
			}
			if _, err := os.Stat(filepath.Join(dir, "started")); !os.IsNotExist(err) {
				t.Fatal("timed-out creation was followed by start")
			}
		})
	}
}

func TestStartupHostArtifactFailureStillStopsAndRemovesContainer(t *testing.T) {
	for _, artifact := range []string{"stop.log", "cleanup.log", "container.log", "run.tsv"} {
		t.Run(artifact, func(t *testing.T) {
			dir, tool := newBoundedSmokeFixture(t)
			wrapper := filepath.Join(dir, "artifact-fault-tool")
			stub := `#!/bin/sh
set -eu
if [ "$1" = start ]; then
  # Make the output filename a directory: writing fails even with root tests.
  for output in "$FIXTURE"/nut-startup.*; do
    mkdir "$output/$FAULT_ARTIFACT"
  done
fi
exec "$REAL_CONTAINER_TOOL" "$@"
`
			if err := os.WriteFile(wrapper, []byte(stub), 0o700); err != nil {
				t.Fatal(err)
			}
			runBoundedSmokeFixture(t, "nut-startup-smoke.sh", dir, wrapper, false, 1,
				"REAL_CONTAINER_TOOL="+tool, "FAULT_ARTIFACT="+artifact)
			assertSmokeOwnedRemoval(t, dir)
			stopped, err := os.ReadFile(filepath.Join(dir, "stopped-name"))
			if err != nil || string(stopped) != "fixture-owned-id\n" {
				t.Fatalf("artifact failure skipped stopping the owned container: %q, %v", stopped, err)
			}
			if output, err := exec.Command("grep", "-F", "could not write startup artifact: "+artifact, filepath.Join(dir, "harness.log")).CombinedOutput(); err != nil {
				t.Fatalf("artifact failure was not reported: %v, %s", err, output)
			}
			if artifact != "run.tsv" {
				matches, err := filepath.Glob(filepath.Join(dir, "nut-startup.*", "run.tsv"))
				if err != nil || len(matches) != 1 {
					t.Fatalf("log-write failure lost metadata: %v, %v", matches, err)
				}
				assertSmokeArtifactLine(t, matches[0], "exit_code\t1")
				assertSmokeArtifactLine(t, matches[0], "container_removed\t1")
			}
		})
	}
}
