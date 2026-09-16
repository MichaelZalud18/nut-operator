//go:build talos

package talos

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// talosHost is the bare address every talosctl invocation in this package targets. Talos
// documentation's own examples always pass --nodes/--endpoints a bare host
// (e.g. "talosctl bootstrap --nodes $CONTROL_PLANE_IP"), never a host:port pair, and rely on the
// client assuming apid's default port (50000) -- which adapter.go's fixed-port forwarding exists
// specifically to make true here.
const talosHost = "127.0.0.1"

// runTalosctl runs a talosctl subcommand bounded by a fresh short-lived context, returning
// trimmed combined output. Every call site supplies its own args; this only owns the timeout and
// error wrapping, the same division test/hadron's runKubectl/runKubectlOutput use for kubectl.
func runTalosctl(ctx context.Context, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "talosctl", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("talosctl %s: %w\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

// talosMaintenanceAPIReachable checks whether the guest's Talos API answers in maintenance mode
// (--insecure): per Talos's own documentation, --insecure cannot be combined with an --endpoints
// override, so this dials talosHost directly on apid's default port. Any successful reply proves
// the guest has booted, not merely that QEMU started -- unlike test/hadron's SSH-reachability
// check, there is no login to prove here, only that apid itself is up.
func talosMaintenanceAPIReachable(ctx context.Context) error {
	_, err := runTalosctl(ctx, 15*time.Second, "version", "--insecure", "--nodes", talosHost)
	return err
}

// genConfig runs `talosctl gen config`, writing controlplane.yaml/worker.yaml/talosconfig into
// outDir, and returns the controlplane config and talosconfig paths this package's other functions
// need. The cluster endpoint is set to KubeAPIAddr directly -- this test's own forwarded loopback
// address -- rather than the guest's own real IP, with that same address repeated via
// --additional-sans so the certificate the endpoint would otherwise reject as unrecognized
// actually validates against it. --install-disk matches the disk name every PEG QEMU machine gets
// by default (types.DefaultDriveSize's own single virtio-blk-pci disk), the same /dev/vda name
// test/hadron's own KairosAutoInstallCloudConfig already targets on the identical PEG-attached
// disk.
func genConfig(ctx context.Context, clusterName, outDir string) (controlplaneConfigPath, talosconfigPath string, err error) {
	if _, err := runTalosctl(ctx, 30*time.Second, "gen", "config", clusterName,
		"https://"+KubeAPIAddr,
		"--additional-sans", talosHost,
		"--install-disk", "/dev/vda",
		"--output-dir", outDir,
	); err != nil {
		return "", "", err
	}
	return filepath.Join(outDir, "controlplane.yaml"), filepath.Join(outDir, "talosconfig"), nil
}

// applyConfig applies controlplaneConfigPath to the guest over the insecure maintenance API,
// installing Talos to disk and rebooting into the configured, certificate-secured system --
// mirroring how Kairos's own auto-install cloud-config triggers an install-then-reboot on first
// boot, just over Talos's API instead of a cloud-init stage.
func applyConfig(ctx context.Context, controlplaneConfigPath string) error {
	_, err := runTalosctl(ctx, 30*time.Second, "apply-config", "--insecure",
		"--nodes", talosHost, "--file", controlplaneConfigPath)
	return err
}

// talosSecureAPIReachable checks whether the guest's Talos API answers using talosconfigPath's
// real client certificate, with no --insecure fallback. A successful reply here -- unlike
// talosMaintenanceAPIReachable's -- proves the applied config actually took effect: the guest
// installed to disk, rebooted, and is running with the certificate genConfig generated, not still
// serving the unauthenticated maintenance API applyConfig was talking to a moment before.
func talosSecureAPIReachable(ctx context.Context, talosconfigPath string) error {
	_, err := runTalosctl(ctx, 15*time.Second, "--talosconfig", talosconfigPath,
		"--nodes", talosHost, "--endpoints", talosHost, "version")
	return err
}

// bootstrap initializes etcd on this single control-plane node. Talos's own documentation is
// explicit this runs exactly once, only after the node's secure API is confirmed reachable --
// callers must not retry this call itself on failure the way waitForWithDiagnostics retries a
// plain readiness check, since a partially-succeeded bootstrap is not idempotent to repeat blindly.
func bootstrap(ctx context.Context, talosconfigPath string) error {
	if _, err := runTalosctl(ctx, 15*time.Second, "--talosconfig", talosconfigPath,
		"config", "endpoints", talosHost); err != nil {
		return fmt.Errorf("setting talosconfig endpoint: %w", err)
	}
	_, err := runTalosctl(ctx, 30*time.Second, "--talosconfig", talosconfigPath,
		"--nodes", talosHost, "bootstrap")
	return err
}

// fetchKubeconfig downloads the bootstrapped cluster's kubeconfig into outDir and returns its
// path. Because genConfig already set the cluster endpoint to KubeAPIAddr, the returned
// kubeconfig's own "server:" line already reads that address directly -- unlike test/hadron's own
// Kubeconfig, nothing here needs a post-hoc port rewrite.
func fetchKubeconfig(ctx context.Context, talosconfigPath, outDir string) (string, error) {
	if _, err := runTalosctl(ctx, 30*time.Second, "--talosconfig", talosconfigPath,
		"--nodes", talosHost, "kubeconfig", outDir); err != nil {
		return "", err
	}
	return filepath.Join(outDir, "kubeconfig"), nil
}
