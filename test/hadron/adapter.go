//go:build hadron
// +build hadron

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

// Package hadron is the thin adapter around github.com/spectrocloud/peg's QEMU machine backend
// that VM-2 (Hadron VM Test Coverage, docs/tasks.md) evaluated before writing any custom VM
// lifecycle code. See docs/contributing/audits/hadron-vm-2-peg-evaluation-2026-09-11.md for why
// PEG was adopted here instead of a second VM framework, and for the gaps in PEG's own defaults
// this package exists to close.
//
// Build-tag gated (`hadron`) the same way test/e2e is gated behind `e2e`: PEG's dependency tree
// (ipfs/go-log, opentracing, and friends) has no reason to be part of the default build, vet, or
// lint graph for a package nothing in the shipped operator imports.
//
// This package only builds a single, safely-configured VM. The two-node reproducible harness,
// pinned Hadron + k3s artifact, and kubeconfig wiring remain open (VM-2's actual harness).
package hadron

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/cavaliergopher/grab/v3"
	"github.com/phayes/freeport"
	"github.com/spectrocloud/peg/pkg/machine"
	"github.com/spectrocloud/peg/pkg/machine/types"
)

// Config is the caller-supplied, non-safety-sensitive shape of one Hadron guest. Everything
// safety-sensitive -- KVM enablement, credentials, network exposure -- is this package's job, not
// the caller's.
type Config struct {
	// Memory is the qemu -m value, e.g. "4096". Empty defers to PEG's own default ("2048").
	Memory string
	// CPUs is the qemu -smp cores value, e.g. "2". Empty defers to PEG's own default ("2").
	CPUs string
	// ISO is a local path or URL to the Kairos artifact to boot. Empty is valid for callers that
	// attach disks some other way; a URL requires ISOChecksum to be set.
	ISO string
	// ISOChecksum pins the ISO's contents (`sha256:<hex>`, or bare hex for sha256). Required
	// whenever ISO is a URL -- VM-2 requires pinned artifact checksums, not a fetch-and-hope.
	ISOChecksum string
	// CloudConfig, if set, is called with this machine's freshly generated Credentials -- which
	// do not exist yet at the time a caller builds a Config, since NewSafeMachine is what
	// generates them -- and its result is rendered into a cloud-init NoCloud seed ISO (volume
	// label "cidata") and attached as PEG's DataSource. Nil means no seed is attached at all --
	// this package takes no position on whether a guest needs one.
	// KairosAutoInstallCloudConfig matches this signature via a closure over the install device,
	// e.g. `func(c Credentials) string { return KairosAutoInstallCloudConfig(c, "/dev/vda") }`.
	CloudConfig func(Credentials) string
	// ClusterNIC, if set, attaches this guest to a private two-node link built by NewClusterLink
	// -- one side Server, the other Client on the same Link. Nil means this guest has no path to
	// any other guest at all, which is PEG's own default.
	ClusterNIC *ClusterNIC
}

// Credentials is the fresh, per-run SSH login this package generates. Never reuse these across
// runs, and never fall back to a static default: PEG's own upstream usage
// (kairos-io/kairos/tests/tests_suite_test.go) defaults to a static "kairos"/"kairos" login, which
// is fine for their own isolated runners but not something this project reuses.
type Credentials struct {
	User string
	Pass string
	// Port is the host-side forwarded SSH port. It is bound to 127.0.0.1 only -- see NewSafeMachine.
	Port string
}

// NewSafeMachine builds (but does not start -- call Create yourself) a PEG QEMU machine with
// every safety property VM-2 requires that PEG does not provide on its own:
//
//   - KVM is required, not attempted-then-silently-dropped: `-enable-kvm` is passed explicitly,
//     which -- like VM-1's own `-accel kvm` probe -- makes QEMU refuse to start rather than fall
//     back to software emulation if it turns out not to be available. Only x86_64 is ever
//     requested: VM-1 proved KVM-accelerated boot on that architecture on GitHub-hosted runners
//     specifically, and PEG's aarch64 path hardcodes TCG with no override.
//   - The SSH login is a fresh random credential, never a static default.
//   - The forwarded SSH port is bound to 127.0.0.1 only. PEG's own default networking
//     (`user,hostfwd=tcp::PORT-:22`, no bind address) listens on every interface; this disables
//     that default (DisableDefaultNetworking) and supplies a loopback-bound equivalent instead.
//   - State is allocated in a private temporary directory and removed if construction fails.
//
// Downloads have a ten-minute deadline. Use NewSafeMachineContext for earlier cancellation.
func NewSafeMachine(cfg Config) (types.Machine, Credentials, error) {
	return NewSafeMachineContext(context.Background(), cfg)
}

// NewSafeMachineContext downloads and verifies the ISO before giving PEG a local path.
// This avoids PEG's panic on transport errors and its fail-open checksum algorithm handling.
func NewSafeMachineContext(ctx context.Context, cfg Config) (m types.Machine, creds Credentials, retErr error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, Credentials{}, err
	}
	var digest []byte
	if cfg.ISOChecksum != "" {
		var err error
		digest, err = parseISOChecksum(cfg.ISOChecksum)
		if err != nil {
			return nil, Credentials{}, err
		}
		cfg.ISOChecksum = "sha256:" + hex.EncodeToString(digest)
	}
	artifact, err := url.Parse(cfg.ISO)
	if err != nil {
		return nil, Credentials{}, fmt.Errorf("parsing ISO location: %w", err)
	}
	remote := artifact.Scheme != ""
	if remote && ((artifact.Scheme != "https" && artifact.Scheme != "http") || artifact.Host == "") {
		return nil, Credentials{}, fmt.Errorf("remote ISO must use an HTTP or HTTPS URL")
	}
	if remote && len(digest) == 0 {
		return nil, Credentials{}, fmt.Errorf("remote ISO requires a pinned SHA-256 checksum")
	}
	creds, err = freshCredentials()
	if err != nil {
		return nil, Credentials{}, fmt.Errorf("generating credentials: %w", err)
	}

	port, err := freeport.GetFreePort()
	if err != nil {
		return nil, Credentials{}, fmt.Errorf("allocating SSH port: %w", err)
	}
	creds.Port = fmt.Sprint(port)

	stateDir, err := os.MkdirTemp("", "nut-operator-hadron-")
	if err != nil {
		return nil, Credentials{}, fmt.Errorf("allocating machine state: %w", err)
	}
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, os.RemoveAll(stateDir))
		}
	}()
	if remote {
		cfg.ISO, err = downloadISO(ctx, cfg.ISO, stateDir, digest)
	} else if cfg.ISO != "" {
		cfg.ISO, err = filepath.Abs(cfg.ISO)
		if err == nil && len(digest) != 0 {
			err = verifyISO(cfg.ISO, digest)
		}
	}
	if err != nil {
		return nil, Credentials{}, err
	}

	var dataSource string
	if cfg.CloudConfig != nil {
		dataSource, err = buildNoCloudISO(stateDir, cfg.CloudConfig(creds))
		if err != nil {
			return nil, Credentials{}, fmt.Errorf("building cloud-init seed: %w", err)
		}
	}

	opts := []types.MachineOption{
		types.QEMUEngine,
		types.WithStateDir(stateDir),
		types.WithArch("x86_64"),
		types.WithCPUType("host"),
		types.DisableDefaultNetworking,
		types.WithSSHUser(creds.User),
		types.WithSSHPass(creds.Pass),
		types.WithSSHPort(creds.Port),
		withArgs(
			"-enable-kvm",
			"-nic", fmt.Sprintf("user,hostfwd=tcp:127.0.0.1:%s-:22", creds.Port),
		),
	}

	if cfg.Memory != "" {
		opts = append(opts, types.WithMemory(cfg.Memory))
	}
	if cfg.CPUs != "" {
		opts = append(opts, types.WithCPU(cfg.CPUs))
	}
	if cfg.ISO != "" {
		opts = append(opts, types.WithISO(cfg.ISO), types.WithISOChecksum(cfg.ISOChecksum))
	}
	if dataSource != "" {
		opts = append(opts, types.WithDataSource(dataSource))
	}
	if cfg.ClusterNIC != nil {
		opts = append(opts, cfg.ClusterNIC.option())
	}

	m, err = machine.New(opts...)
	if err != nil {
		return nil, Credentials{}, fmt.Errorf("configuring machine: %w", err)
	}
	return m, creds, nil
}

func downloadISO(ctx context.Context, location, stateDir string, digest []byte) (string, error) {
	path := filepath.Join(stateDir, "boot.iso")
	req, err := grab.NewRequest(path, location)
	if err != nil {
		return "", fmt.Errorf("creating ISO request: %w", err)
	}
	req = req.WithContext(ctx)
	req.NoResume = true
	req.SetChecksum(sha256.New(), digest, true)
	if err := grab.NewClient().Do(req).Err(); err != nil {
		return "", fmt.Errorf("downloading ISO: %w", err)
	}
	return path, nil
}

func verifyISO(path string, digest []byte) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening ISO: %w", err)
	}
	defer func() { _ = f.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return fmt.Errorf("reading ISO: %w", err)
	}
	if !bytes.Equal(hash.Sum(nil), digest) {
		return fmt.Errorf("ISO SHA-256 checksum mismatch")
	}
	return nil
}

func parseISOChecksum(value string) ([]byte, error) {
	algorithm, digest, prefixed := strings.Cut(value, ":")
	if !prefixed {
		digest = value
	} else if !strings.EqualFold(algorithm, "sha256") {
		return nil, fmt.Errorf("ISO checksum must use sha256")
	}
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != sha256.Size {
		return nil, fmt.Errorf("ISO checksum must contain exactly 64 hexadecimal SHA-256 digits")
	}
	return decoded, nil
}

// SafeTeardown retains an OS process handle before Stop can delete PEG's PID file. It only
// removes state once that original process has exited. Missing or invalid process evidence
// fails closed; callers must clean never-started machines separately, when no process exists.
//
// Stop() itself is host-driven process termination, not a guest-cooperative shutdown -- VM-3's
// own shutdown-evidence design already accounts for this and must never treat this call's success
// as proof of anything the actuator did.
func SafeTeardown(m types.Machine, timeout time.Duration) error {
	if timeout <= 0 {
		return fmt.Errorf("teardown timeout must be positive")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	p, err := machineProcess(m)
	if err != nil {
		return err
	}
	defer func() { _ = p.Release() }()
	if err := p.Signal(syscall.Signal(0)); processExited(err) {
		return m.Clean()
	} else if err != nil {
		return fmt.Errorf("checking machine process: %w", err)
	}
	stopErr := m.Stop()
	if err := waitForProcessExit(ctx, p); err != nil {
		return errors.Join(stopErr, fmt.Errorf("refusing to remove machine state: %w", err))
	}
	return errors.Join(stopErr, m.Clean())
}

// SafeStop retains diagnostic state and verifies exit through the original process handle.
// Unlike PEG Stop, this does not spawn an unbounded external kill command or delete the PID file.
func SafeStop(m types.Machine, timeout time.Duration) error {
	if timeout <= 0 {
		return fmt.Errorf("stop timeout must be positive")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	p, err := machineProcess(m)
	if err != nil {
		return err
	}
	defer func() { _ = p.Release() }()
	if err := p.Kill(); err != nil && !processExited(err) {
		return err
	}
	return waitForProcessExit(ctx, p)
}

func machineProcess(m types.Machine) (*os.Process, error) {
	if m == nil || m.Config().StateDir == "" {
		return nil, fmt.Errorf("machine state directory is required")
	}
	data, err := os.ReadFile(filepath.Join(m.Config().StateDir, "pid"))
	if err != nil {
		return nil, fmt.Errorf("reading machine PID before teardown: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 || pid == os.Getpid() {
		return nil, fmt.Errorf("invalid machine PID; refusing teardown")
	}
	return os.FindProcess(pid)
}

func processExited(err error) bool {
	return errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH)
}

func waitForProcessExit(ctx context.Context, p *os.Process) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := p.Signal(syscall.Signal(0)); processExited(err) {
			return nil
		} else if err != nil {
			return fmt.Errorf("checking machine process: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// withArgs is a types.MachineOption that appends raw QEMU arguments. The types package has no
// built-in option for this as of the pinned commit -- MachineConfig.Args is only reachable by
// direct field access, which this constructs as an option so it composes with the rest of the
// With* option list.
func withArgs(args ...string) types.MachineOption {
	return func(mc *types.MachineConfig) error {
		mc.Args = append(mc.Args, args...)
		return nil
	}
}

func freshCredentials() (Credentials, error) {
	pass, err := randomToken(20)
	if err != nil {
		return Credentials{}, err
	}
	return Credentials{User: "hadron", Pass: pass}, nil
}

// randomToken returns an n-byte random value, lowercase-base32-encoded so it is always safe to
// embed directly in a shell command or QEMU argument with no further escaping.
func randomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf)), nil
}
