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
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

const sampleK3sKubeconfig = `apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: LS0tLS1CRUdJTi...
    server: https://127.0.0.1:6443
  name: default
contexts:
- context:
    cluster: default
    user: default
  name: default
current-context: default
kind: Config
users:
- name: default
  user:
    client-certificate-data: LS0tLS1CRUdJTi...
    client-key-data: LS0tLS1CRUdJTi...
`

func TestRewriteKubeconfigServerPortOnlyChangesThePort(t *testing.T) {
	rewritten, err := rewriteKubeconfigServerPort(sampleK3sKubeconfig, "34567")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rewritten, "server: https://127.0.0.1:34567") {
		t.Errorf("expected the rewritten port, got:\n%s", rewritten)
	}
	if strings.Contains(rewritten, ":6443") {
		t.Errorf("original port still present:\n%s", rewritten)
	}
	// Nothing else about the document should move -- same line count, same certificate data.
	if strings.Count(rewritten, "\n") != strings.Count(sampleK3sKubeconfig, "\n") {
		t.Error("rewrite changed the number of lines")
	}
	if !strings.Contains(rewritten, "certificate-authority-data: LS0tLS1CRUdJTi...") {
		t.Error("rewrite touched certificate data it should not have")
	}
}

func TestRewriteKubeconfigServerPortRejectsMissingServerLine(t *testing.T) {
	if _, err := rewriteKubeconfigServerPort("not a kubeconfig at all", "34567"); err == nil {
		t.Fatal("expected an error for a document with no server: line")
	}
}

func TestKubeconfigRequiresForwardedAPIPort(t *testing.T) {
	if _, err := Kubeconfig(context.Background(), Credentials{Port: "2222"}); err == nil {
		t.Fatal("expected an error when Credentials.KubeAPIPort is empty")
	}
}

func TestKubeconfigFetchesAndRewrites(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		server, channels, requests, err := ssh.NewServerConn(conn, config)
		if err != nil {
			return
		}
		defer func() { _ = server.Close() }()
		go ssh.DiscardRequests(requests)
		for newChannel := range channels {
			channel, reqs, err := newChannel.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = channel.Close() }()
				for req := range reqs {
					if req.Type == "exec" {
						_, _ = channel.Write([]byte(sampleK3sKubeconfig))
						_ = req.Reply(true, nil)
						_, _ = channel.SendRequest("exit-status", false, []byte{0, 0, 0, 0})
						return
					}
					_ = req.Reply(false, nil)
				}
			}()
		}
	}()

	_, port, _ := net.SplitHostPort(listener.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	kubeconfig, err := Kubeconfig(ctx, Credentials{User: "test", Port: port, KubeAPIPort: "45678"})
	if err != nil {
		t.Fatalf("Kubeconfig: %v", err)
	}
	if !strings.Contains(kubeconfig, "server: https://127.0.0.1:45678") {
		t.Errorf("expected the rewritten port in fetched kubeconfig, got:\n%s", kubeconfig)
	}
}
