package nutsupervisor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Fake NUT commands use private fixture files for fault injection. They never
// parse production configuration; image tests cover actual NUT reload decisions.
type driverSupervisorHarness struct {
	t         *testing.T
	root      string
	etcDir    string
	behaveDir string
	binDir    string
	upsConf   string
	upsdUsers string
}

func newDriverSupervisorHarness(t *testing.T) *driverSupervisorHarness {
	t.Helper()
	root := t.TempDir()
	h := &driverSupervisorHarness{t: t, root: root, etcDir: filepath.Join(root, "config"),
		behaveDir: filepath.Join(root, "behavior"), binDir: filepath.Join(root, "bin")}
	h.upsConf, h.upsdUsers = filepath.Join(h.etcDir, "ups.conf"), filepath.Join(h.etcDir, "upsd.users")
	for _, dir := range []string{h.etcDir, h.behaveDir, h.binDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	atomicWriteFile(t, h.upsdUsers, []byte("initial\n"), 0o600)
	h.mustWriteExecutable(filepath.Join(h.binDir, "upsdrvctl"), `#!/bin/sh
behave_dir="`+h.behaveDir+`"
case "$1" in
  list)
    [ ! -f "$behave_dir/.list_fail" ] || exit 1
    [ -s "$behave_dir/.configured" ] || exit 1
    cat "$behave_dir/.configured"
    ;;
  -FF)
    ups="$3"
    behavior=run
    [ ! -f "$behave_dir/$ups.behavior" ] || behavior="$(cat "$behave_dir/$ups.behavior")"
    case "$behavior" in
      crash) exit 7;;
      run)
        trap ':' USR1
        trap 'kill "$sleeper" 2>/dev/null || true; wait "$sleeper" 2>/dev/null || true; exit 0' TERM INT
        echo "$$" > "$behave_dir/$ups.child"
        while :; do sleep 60 & sleeper=$!; wait "$sleeper"; done
        ;;
      *) exit 2;;
    esac
    ;;
  -c)
    printf '%s\n' "$*" >> "`+filepath.Join(h.root, "driver-reload.log")+`"
    ;;
  *) exit 2;;
esac
`)
	h.mustWriteExecutable(filepath.Join(h.binDir, "upsd"), `#!/bin/sh
if [ "$1" = -c ] && [ "$2" = reload ]; then
  echo reload >> "`+filepath.Join(h.root, "upsd-reload.log")+`"
  [ ! -f "`+filepath.Join(h.root, "upsd-reload-fail")+`" ] || exit 1
  exit 0
fi
exit 2
`)
	return h
}

func (h *driverSupervisorHarness) mustWriteExecutable(path, content string) {
	h.t.Helper()
	atomicWriteFile(h.t, path, []byte(content), 0o755)
}

func atomicWriteFile(t *testing.T, path string, content []byte, perm os.FileMode) {
	t.Helper()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, perm); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func (h *driverSupervisorHarness) setConfiguredUPS(names ...string) {
	h.t.Helper()
	atomicWriteFile(h.t, filepath.Join(h.behaveDir, ".configured"), []byte(strings.Join(names, "\n")), 0o600)
	var conf strings.Builder
	for _, name := range names {
		fmt.Fprintf(&conf, "[%s]\n  driver = dummy-ups\n", name)
	}
	atomicWriteFile(h.t, h.upsConf, []byte(conf.String()), 0o600)
}

func (h *driverSupervisorHarness) touchServerConfig(marker string) {
	h.t.Helper()
	atomicWriteFile(h.t, h.upsdUsers, []byte(marker+"\n"), 0o600)
}

func (h *driverSupervisorHarness) setBehavior(ups, behavior string) {
	h.t.Helper()
	atomicWriteFile(h.t, filepath.Join(h.behaveDir, ups+".behavior"), []byte(behavior), 0o600)
}

func (h *driverSupervisorHarness) failListing(fail bool) {
	h.t.Helper()
	marker := filepath.Join(h.behaveDir, ".list_fail")
	if !fail {
		if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
			h.t.Fatal(err)
		}
		return
	}
	atomicWriteFile(h.t, marker, nil, 0o600)
}

func childPID(t *testing.T, h *driverSupervisorHarness, name string) int {
	t.Helper()
	var pid int
	eventually(t, time.Second, func() bool {
		data, err := os.ReadFile(filepath.Join(h.behaveDir, name+".child"))
		if err != nil {
			return false
		}
		_, err = fmt.Sscanf(string(data), "%d", &pid)
		return err == nil && syscall.Kill(pid, 0) == nil
	})
	return pid
}

func eventually(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("condition was not met within %s", timeout)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
