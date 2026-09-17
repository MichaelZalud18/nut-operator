//go:build talos

package talos

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withFakeTalosctl prepends a directory containing a fake, executable "talosctl" script to PATH
// for the duration of the test, restoring PATH afterward. script is the shell script body; it
// receives talosctl's own argv as "$@". This exists so this package's exact argv construction --
// the actual protocol-correctness risk in talosctl.go, since none of it has run against a real
// guest yet -- has a regression test that does not require a live Talos API or KVM.
func withFakeTalosctl(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "talosctl")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil { //nolint:gosec // deliberately executable fake CLI for this test only
		t.Fatalf("writing fake talosctl: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestRunTalosctlSuccess(t *testing.T) {
	withFakeTalosctl(t, `echo "  hello  "`)
	out, err := runTalosctl(context.Background(), time.Second, "version")
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello" {
		t.Fatalf("got %q, want trimmed %q", out, "hello")
	}
}

func TestRunTalosctlFailureIncludesOutput(t *testing.T) {
	withFakeTalosctl(t, `echo "boom" >&2; exit 1`)
	_, err := runTalosctl(context.Background(), time.Second, "version")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected error containing the command's own output, got %v", err)
	}
}

func TestTalosMaintenanceAPIReachableArgs(t *testing.T) {
	withFakeTalosctl(t, `echo "$@"`)
	if err := talosMaintenanceAPIReachable(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestTalosMaintenanceAPIReachablePropagatesFailure(t *testing.T) {
	withFakeTalosctl(t, `exit 1`)
	if err := talosMaintenanceAPIReachable(context.Background()); err == nil {
		t.Fatal("expected an error")
	}
}

// TestGenConfigArgs asserts on the exact flags genConfig passes -- --additional-sans and
// --install-disk in particular have no live-guest evidence yet that they are spelled or ordered
// correctly, so this is the regression test that exists instead of one.
func TestTalosSecureAPIReachableNeverPassesInsecure(t *testing.T) {
	outDir := t.TempDir()
	withFakeTalosctl(t, `echo "$@" > `+outDir+`/argv`)
	if err := talosSecureAPIReachable(context.Background(), filepath.Join(outDir, "talosconfig")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(outDir, "argv"))
	if err != nil {
		t.Fatal(err)
	}
	argv := string(raw)
	if strings.Contains(argv, "--insecure") {
		t.Errorf("argv %q must not pass --insecure once the guest has a real certificate", argv)
	}
	if !strings.Contains(argv, "--endpoints "+talosHost) {
		t.Errorf("argv %q missing --endpoints %s", argv, talosHost)
	}
}

func TestGenConfigArgs(t *testing.T) {
	var captured string
	outDir := t.TempDir()
	withFakeTalosctl(t, `echo "$@" > `+outDir+`/argv; touch "${12}/controlplane.yaml" "${12}/talosconfig"`)
	controlplaneConfigPath, talosconfigPath, err := genConfig(context.Background(), "nut-operator-talos-smoke", outDir)
	if err != nil {
		t.Fatal(err)
	}
	if controlplaneConfigPath != filepath.Join(outDir, "controlplane.yaml") {
		t.Fatalf("unexpected controlplane path: %s", controlplaneConfigPath)
	}
	if talosconfigPath != filepath.Join(outDir, "talosconfig") {
		t.Fatalf("unexpected talosconfig path: %s", talosconfigPath)
	}
	raw, err := os.ReadFile(filepath.Join(outDir, "argv"))
	if err != nil {
		t.Fatal(err)
	}
	captured = strings.TrimSpace(string(raw))
	for _, want := range []string{
		"gen config nut-operator-talos-smoke https://" + KubeAPIAddr,
		"--additional-sans " + talosHost,
		"--install-disk /dev/vda",
		`--config-patch {"cluster":{"allowSchedulingOnControlPlanes":true}}`,
		"--output-dir " + outDir,
	} {
		if !strings.Contains(captured, want) {
			t.Errorf("argv %q does not contain %q", captured, want)
		}
	}
}

func TestGenConfigArgsWithRegistryMirror(t *testing.T) {
	outDir := t.TempDir()
	withFakeTalosctl(t, `echo "$@" > `+outDir+`/argv; touch "${12}/controlplane.yaml" "${12}/talosconfig"`)
	if _, _, err := genConfig(context.Background(), "nut-operator-talos-smoke", outDir, "10.0.2.2:5000=http://10.0.2.2:5000"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(outDir, "argv"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "--registry-mirror 10.0.2.2:5000=http://10.0.2.2:5000") {
		t.Fatalf("argv %q missing --registry-mirror", string(raw))
	}
}

func TestApplyConfigArgsNeverPassEndpointsWithInsecure(t *testing.T) {
	// Talos's own documentation: "--insecure cannot be combined with an --endpoints override."
	outDir := t.TempDir()
	withFakeTalosctl(t, `echo "$@" > `+outDir+`/argv`)
	if err := applyConfig(context.Background(), filepath.Join(outDir, "controlplane.yaml")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(outDir, "argv"))
	if err != nil {
		t.Fatal(err)
	}
	argv := string(raw)
	if !strings.Contains(argv, "--insecure") {
		t.Errorf("argv %q missing --insecure", argv)
	}
	if strings.Contains(argv, "--endpoints") {
		t.Errorf("argv %q must not pass --endpoints alongside --insecure", argv)
	}
}

func TestBootstrapSetsEndpointsBeforeBootstrapping(t *testing.T) {
	outDir := t.TempDir()
	withFakeTalosctl(t, `echo "$@" >> `+outDir+`/argv`)
	if err := bootstrap(context.Background(), filepath.Join(outDir, "talosconfig")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(outDir, "argv"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected two talosctl invocations (config endpoints, then bootstrap), got %d: %q", len(lines), raw)
	}
	if !strings.Contains(lines[0], "config endpoints") {
		t.Errorf("first call should set endpoints, got %q", lines[0])
	}
	if !strings.Contains(lines[1], "bootstrap") {
		t.Errorf("second call should bootstrap, got %q", lines[1])
	}
}

func TestFetchKubeconfigReturnsExpectedPath(t *testing.T) {
	outDir := t.TempDir()
	withFakeTalosctl(t, `true`)
	path, err := fetchKubeconfig(context.Background(), filepath.Join(outDir, "talosconfig"), outDir)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(outDir, "kubeconfig") {
		t.Fatalf("unexpected kubeconfig path: %s", path)
	}
}

func TestFetchKubeconfigPropagatesFailure(t *testing.T) {
	withFakeTalosctl(t, `exit 1`)
	if _, err := fetchKubeconfig(context.Background(), "talosconfig", t.TempDir()); err == nil {
		t.Fatal("expected an error")
	}
}
