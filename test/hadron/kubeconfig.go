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

package hadron

import (
	"context"
	"fmt"
	"regexp"
)

const k3sKubeconfigPath = "/etc/rancher/k3s/k3s.yaml"

// kubeconfigServerPortRE matches the port in a k3s kubeconfig's "server: https://<host>:<port>"
// line, capturing everything up to and including the colon so only the port digits are replaced.
var kubeconfigServerPortRE = regexp.MustCompile(`(?m)^(\s*server:\s*https://[^:\s]+:)\d+\b`)

// Kubeconfig fetches the guest's own k3s-generated kubeconfig over SSH and rewrites its server
// URL's port from k3s's internal 6443 to Credentials.KubeAPIPort -- the host-side loopback port
// NewSafeMachine forwarded when Config.ForwardKubeAPI was set.
//
// Only the port changes, deliberately: k3s's default kubeconfig already targets "127.0.0.1" --
// proven working in VM-2's own live smoke test, which ran `sudo k3s kubectl get nodes`
// successfully inside the guest against that exact default -- and QEMU's hostfwd only remaps
// ports, never addresses. The server certificate's Subject Alternative Name check only ever
// inspects the host/IP portion of the URL, not the port, so rewriting the port alone needs no
// certificate changes and no -k3s-server startup flags.
//
// Requires Config.ForwardKubeAPI; returns an error otherwise, since there is no forwarded port to
// rewrite to. Not yet exercised against a real guest -- see network.go's own equivalent note for
// the reused precedent (a hostfwd port-remap already proven for SSH) this reasoning rests on.
func Kubeconfig(ctx context.Context, creds Credentials) (string, error) {
	if creds.KubeAPIPort == "" {
		return "", fmt.Errorf("kubeconfig: Config.ForwardKubeAPI was not set; no forwarded API port to rewrite to")
	}
	raw, err := guestCommand(ctx, creds, "sudo cat "+k3sKubeconfigPath)
	if err != nil {
		return "", fmt.Errorf("fetching %s: %w", k3sKubeconfigPath, err)
	}
	return rewriteKubeconfigServerPort(raw, creds.KubeAPIPort)
}

// rewriteKubeconfigServerPort is a pure function so the rewrite logic is testable without SSH or
// a real guest.
func rewriteKubeconfigServerPort(kubeconfig, port string) (string, error) {
	if !kubeconfigServerPortRE.MatchString(kubeconfig) {
		return "", fmt.Errorf(`kubeconfig: no "server: https://<host>:<port>" line found`)
	}
	return kubeconfigServerPortRE.ReplaceAllString(kubeconfig, "${1}"+port), nil
}
