//go:build talos
// +build talos

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

// Package talos is VM-7's guest adapter: the same PEG/QEMU machine backend test/hadron already
// adopted (docs/contributing/audits/hadron-vm-2-peg-evaluation-2026-09-11.md), reused directly
// rather than through a second VM framework, matching VM-7's own text: "qualify a Talos guest
// using the existing PEG/QEMU harness, not a parallel Talos framework."
//
// This package is deliberately not built on test/hadron's own Config/Credentials: Talos exposes no
// SSH at all (docs/tasks.md's VM-8 names this precisely -- "the generic layer must not assume SSH
// exists"), and has no cloud-init equivalent -- machine configuration is applied after boot, over
// Talos's own gRPC API (see talosctl.go), not baked into a NoCloud seed ISO. Reaching for
// test/hadron's Config here would mean adding SSH- and cloud-init-shaped fields a Talos guest
// cannot use, not reusing a genuinely shared shape. The two packages share only the underlying PEG
// library and its ISO-download/verify/teardown primitives, which are duplicated below in the same
// small, self-contained form test/hadron already proved -- promoting them into one shared package
// is VM-8's own extraction to do once a third guest adapter makes the overlap self-evident, not a
// speculative abstraction over two.
//
// Build-tag gated (`talos`), the same reasoning as `hadron`: PEG's dependency tree has no reason to
// be part of the default build/vet/lint graph for a package nothing in the shipped operator
// imports.
package talos

import (
	"bytes"
	"context"
	"crypto/sha256"
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
	"github.com/spectrocloud/peg/pkg/machine"
	"github.com/spectrocloud/peg/pkg/machine/types"
)

// TalosAPIAddr is the host-side loopback address this package always forwards the guest's Talos
// API (apid, container port 50000) to. Fixed rather than allocated per-run like test/hadron's SSH
// port: talosctl's --nodes/--endpoints addressing assumes apid's default port (50000) unless a
// client explicitly overrides it, and every command in talosctl.go would otherwise need to carry a
// per-run port override whose exact flag syntax has not been proven live. One guest per test
// avoids the collision a fixed port would otherwise risk.
const TalosAPIAddr = "127.0.0.1:50000"

// KubeAPIAddr is the host-side loopback address the guest's Kubernetes API (container port 6443)
// is always forwarded to, for the same fixed-port reasoning as TalosAPIAddr. Used verbatim as the
// cluster endpoint passed to `talosctl gen config`, so the generated kubeconfig's own `server:`
// line already reads this address -- unlike test/hadron's Kubeconfig, which has to rewrite a
// k3s-internal port after the fact, nothing here needs a post-hoc rewrite.
const KubeAPIAddr = "127.0.0.1:6443"

// Config is the caller-supplied, non-safety-sensitive shape of one Talos guest.
type Config struct {
	// Memory is the qemu -m value, e.g. "4096". Empty defers to PEG's own default ("2048").
	Memory string
	// CPUs is the qemu -smp cores value, e.g. "2". Empty defers to PEG's own default ("2").
	CPUs string
	// ISO is a local path or URL to the Talos metal ISO to boot.
	ISO string
	// ISOChecksum pins the ISO's contents (`sha256:<hex>`, or bare hex for sha256). Required
	// whenever ISO is a URL -- the same pinned-artifact requirement test/hadron's VM-2 harness
	// already enforces.
	ISOChecksum string
}

// NewSafeMachine builds (but does not start -- call Create yourself) a PEG QEMU machine with the
// same safety properties test/hadron's NewSafeMachine establishes for its own guest -- KVM
// required rather than silently falling back to software emulation, state in a private temporary
// directory removed on construction failure, a pinned/verified ISO -- minus everything specific to
// an SSH-reachable guest. In place of a forwarded SSH port, this forwards the two loopback
// addresses above.
//
// Downloads have a ten-minute deadline. Use NewSafeMachineContext for earlier cancellation.
func NewSafeMachine(cfg Config) (types.Machine, error) {
	return NewSafeMachineContext(context.Background(), cfg)
}

// NewSafeMachineContext downloads and verifies the ISO before giving PEG a local path, the same
// reasoning as test/hadron's own equivalent: this avoids PEG's panic on transport errors and its
// fail-open checksum algorithm handling.
func NewSafeMachineContext(ctx context.Context, cfg Config) (m types.Machine, retErr error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var digest []byte
	if cfg.ISOChecksum != "" {
		var err error
		digest, err = parseISOChecksum(cfg.ISOChecksum)
		if err != nil {
			return nil, err
		}
		cfg.ISOChecksum = "sha256:" + hex.EncodeToString(digest)
	}
	artifact, err := url.Parse(cfg.ISO)
	if err != nil {
		return nil, fmt.Errorf("parsing ISO location: %w", err)
	}
	remote := artifact.Scheme != ""
	if remote && ((artifact.Scheme != "https" && artifact.Scheme != "http") || artifact.Host == "") {
		return nil, fmt.Errorf("remote ISO must use an HTTP or HTTPS URL")
	}
	if remote && len(digest) == 0 {
		return nil, fmt.Errorf("remote ISO requires a pinned SHA-256 checksum")
	}

	stateDir, err := os.MkdirTemp("", "nut-operator-talos-")
	if err != nil {
		return nil, fmt.Errorf("allocating machine state: %w", err)
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
		return nil, err
	}

	opts := []types.MachineOption{
		types.QEMUEngine,
		types.WithStateDir(stateDir),
		types.WithArch("x86_64"),
		types.WithCPUType("host"),
		types.DisableDefaultNetworking,
		withArgs("-enable-kvm", "-nic", managementNIC()),
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

	m, err = machine.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("configuring machine: %w", err)
	}
	return m, nil
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

// SafeTeardown retains an OS process handle before Stop can delete PEG's PID file. It only removes
// state once that original process has exited. Missing or invalid process evidence fails closed;
// callers must clean never-started machines separately, when no process exists. Identical to
// test/hadron's SafeTeardown -- see its own comment for the full PID-reuse rationale.
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
// Identical to test/hadron's SafeStop.
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

// withArgs is a types.MachineOption that appends raw QEMU arguments. Identical to test/hadron's.
func withArgs(args ...string) types.MachineOption {
	return func(mc *types.MachineConfig) error {
		mc.Args = append(mc.Args, args...)
		return nil
	}
}

// managementNIC forwards both loopback addresses this package always uses. QEMU's "-nic"/"-netdev
// user" backend accepts a repeated hostfwd= key for additional port forwards on the same NIC.
func managementNIC() string {
	talosHost, talosPort, _ := strings.Cut(TalosAPIAddr, ":")
	kubeHost, kubePort, _ := strings.Cut(KubeAPIAddr, ":")
	return fmt.Sprintf("user,hostfwd=tcp:%s:%s-:50000,hostfwd=tcp:%s:%s-:6443", talosHost, talosPort, kubeHost, kubePort)
}
