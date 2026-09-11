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
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"strings"
	"time"

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
//   - The state directory is left to PEG's own os.MkdirTemp-backed allocation, which is already
//     unique per process, rather than inventing a second naming scheme that could collide with it.
//
// If cfg.ISO is a URL, PEG downloads and checksum-verifies it synchronously inside this call
// (machine.New(), not the later Create()) -- this can block on network I/O, and a failed
// download is known to panic rather than return an error in the pinned PEG commit (a nil
// dereference in its own download helper on request failure). Callers passing a URL ISO should
// pre-validate reachability or recover() at the call site until upstream fixes this; see
// docs/contributing/audits/hadron-vm-2-peg-evaluation-2026-09-11.md.
func NewSafeMachine(cfg Config) (types.Machine, Credentials, error) {
	creds, err := freshCredentials()
	if err != nil {
		return nil, Credentials{}, fmt.Errorf("generating credentials: %w", err)
	}

	port, err := freeport.GetFreePort()
	if err != nil {
		return nil, Credentials{}, fmt.Errorf("allocating SSH port: %w", err)
	}
	creds.Port = fmt.Sprint(port)

	if cfg.ISO != "" && isURL(cfg.ISO) && cfg.ISOChecksum == "" {
		return nil, Credentials{}, fmt.Errorf("ISO %q is a URL with no checksum; VM-2 requires pinned artifact checksums", cfg.ISO)
	}

	opts := []types.MachineOption{
		types.QEMUEngine,
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

	m, err := machine.New(opts...)
	if err != nil {
		return nil, Credentials{}, fmt.Errorf("configuring machine: %w", err)
	}
	return m, creds, nil
}

// SafeTeardown stops the machine and only removes its state after confirming the underlying
// process has actually exited, bounded by timeout. PEG's own Clean() unconditionally
// os.RemoveAll's the state directory with no check that the process has exited first; skipping
// that confirmation risks removing state (disk images, the monitor socket) out from under a
// process still tearing itself down.
//
// Stop() itself is host-driven process termination, not a guest-cooperative shutdown -- VM-3's
// own shutdown-evidence design already accounts for this and must never treat this call's success
// as proof of anything the actuator did.
func SafeTeardown(m types.Machine, timeout time.Duration) error {
	stopErr := m.Stop()

	if aliver, ok := m.(interface{ Alive() bool }); ok {
		deadline := time.Now().Add(timeout)
		for aliver.Alive() && time.Now().Before(deadline) {
			time.Sleep(500 * time.Millisecond)
		}
		if aliver.Alive() {
			return fmt.Errorf("machine process still alive %s after Stop(); refusing to remove state out from under it", timeout)
		}
	}

	if err := m.Clean(); err != nil {
		if stopErr != nil {
			return fmt.Errorf("stop: %w (clean also failed: %v)", stopErr, err)
		}
		return fmt.Errorf("clean: %w", err)
	}
	return stopErr
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

func isURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}
