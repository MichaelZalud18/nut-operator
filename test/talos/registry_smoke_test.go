//go:build talos_smoke
// +build talos_smoke

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

package talos

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// guestGatewayHost is QEMU user-mode networking's own well-known host-gateway address: the
// default "-nic user" backend's built-in NAT router (SLIRP) always answers here, whatever the
// guest's own DHCP-leased address is (TalosAPIAddr/KubeAPIAddr's forwarded ports are unrelated
// host-side addresses; this one goes the other direction, guest-to-host).
const guestGatewayHost = "10.0.2.2"

// startLocalRegistry runs a throwaway, unauthenticated OCI registry container bound to a
// host-assigned port, torn down at test cleanup. Talos has no SSH and no `ctr images import`
// equivalent test/hadron's own adapter relies on, so this is how a locally built image reaches
// the guest instead: push it here from the host, then point the guest's own machine configuration
// at it via a registry mirror (genConfig's own registryMirrors parameter) using guestGatewayHost,
// which is reachable from inside the guest without any explicit port forward.
func startLocalRegistry(ctx context.Context, t *testing.T) (hostPort string) {
	t.Helper()
	run := exec.CommandContext(ctx, "docker", "run", "-d", "--rm", "-p", "5000", "registry:2")
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("docker run registry:2: %v\n%s", err, out)
	}
	// 2026-09-17 first live run: on a cold image cache, docker run's combined stdout+stderr
	// includes the full pull-progress text ahead of the container ID -- trimming the whole blob
	// captured that text as "containerID", which then failed every subsequent docker command.
	// Only the last line is ever the ID.
	trimmed := strings.TrimSpace(string(out))
	lines := strings.Split(trimmed, "\n")
	containerID := strings.TrimSpace(lines[len(lines)-1])
	if containerID == "" {
		t.Fatalf("docker run registry:2 produced no container ID:\n%s", out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "stop", containerID).Run() })

	portOut, err := exec.CommandContext(ctx, "docker", "port", containerID, "5000/tcp").CombinedOutput()
	if err != nil {
		t.Fatalf("docker port %s: %v\n%s", containerID, err, portOut)
	}
	// "docker port" prints "0.0.0.0:<port>\n[::]:<port>\n" -- the first line's port is what matters,
	// the same host port either address family reaches.
	firstLine := strings.SplitN(strings.TrimSpace(string(portOut)), "\n", 2)[0]
	_, hostPort, ok := strings.Cut(firstLine, ":")
	if !ok || hostPort == "" {
		t.Fatalf("unexpected docker port output: %q", portOut)
	}
	return hostPort
}

// buildAndPushTalosImage builds one of this repository's own operand Dockerfiles
// (images/<name>/Dockerfile) via a plain docker build -- the same command test/hadron's own
// buildOperandImageTarball uses, since operand Dockerfiles need no extra build args -- and pushes
// it into the local registry startLocalRegistry started.
func buildAndPushTalosImage(ctx context.Context, t *testing.T, repoRoot, dockerfileRelPath, namePrefix, registryHostPort string) (guestImageRef string) {
	t.Helper()
	localRef := fmt.Sprintf("localhost:%s/nut-operator-talos-%s-test:%d", registryHostPort, namePrefix, time.Now().UnixNano())
	build := exec.CommandContext(ctx, "docker", "build",
		"-f", filepath.Join(repoRoot, dockerfileRelPath),
		"-t", localRef, repoRoot)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("docker build %s: %v\n%s", dockerfileRelPath, err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", localRef).Run() })
	return pushLocalImage(ctx, t, localRef, registryHostPort)
}

// buildAndPushManagerImage builds the real manager image through this repository's own
// `make docker-build` target -- unlike the operand Dockerfiles, it needs the version/revision/
// provenance build args that target supplies (Makefile's own docker-build recipe), the same
// reasoning test/hadron's own buildManagerImageTarball documents -- then pushes it into the local
// registry startLocalRegistry started.
func buildAndPushManagerImage(ctx context.Context, t *testing.T, repoRoot, registryHostPort string) (guestImageRef string) {
	t.Helper()
	localRef := fmt.Sprintf("localhost:%s/nut-operator-talos-manager-test:%d", registryHostPort, time.Now().UnixNano())
	build := exec.CommandContext(ctx, "make", "docker-build", "IMG="+localRef)
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("make docker-build: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", localRef).Run() })
	return pushLocalImage(ctx, t, localRef, registryHostPort)
}

// pushLocalImage pushes an already-built, already-tagged localhost:<registryHostPort>/... image
// into the local registry and returns the reference the guest itself should pull -- rewritten to
// guestGatewayHost so it resolves from inside the guest, not the host loopback address the push
// step itself used to reach the same registry.
func pushLocalImage(ctx context.Context, t *testing.T, localRef, registryHostPort string) (guestImageRef string) {
	t.Helper()
	push := exec.CommandContext(ctx, "docker", "push", localRef)
	if out, err := push.CombinedOutput(); err != nil {
		t.Fatalf("docker push %s: %v\n%s", localRef, err, out)
	}
	guestRegistry := guestGatewayHost + ":" + registryHostPort
	_, remainder, _ := strings.Cut(localRef, "/")
	return guestRegistry + "/" + remainder
}

// registryMirrorFlag formats a registry host's guest-facing address as the "<host>=<url>" pair
// talosctl gen config's own --registry-mirror flag expects, redirecting pulls for that host to a
// plain HTTP endpoint (no TLS material exists for this ephemeral registry).
func registryMirrorFlag(registryHostPort string) string {
	guestRegistry := guestGatewayHost + ":" + registryHostPort
	return guestRegistry + "=http://" + guestRegistry
}
