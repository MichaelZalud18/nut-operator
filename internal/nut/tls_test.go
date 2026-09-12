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

package nut

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientSTARTTLS(t *testing.T) {
	fixture := httptest.NewTLSServer(nil)
	cert := fixture.TLS.Certificates[0]
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})
	fixture.Close()
	for _, tc := range []struct {
		name            string
		mode            TLSMode
		reply, identity string
		trust           []byte
		wantErr         bool
	}{
		{"required", TLSRequired, "OK STARTTLS", "example.com", ca, false},
		{"opportunistic TLS", TLSOpportunistic, "OK STARTTLS", "example.com", ca, false},
		{"wrong name", TLSRequired, "OK STARTTLS", "wrong.example", ca, true},
		{"opportunistic wrong name", TLSOpportunistic, "OK STARTTLS", "wrong.example", ca, true},
		{"untrusted", TLSRequired, "OK STARTTLS", "example.com", nil, true},
		{"bad CA", TLSRequired, "OK STARTTLS", "example.com", []byte("invalid"), true},
		{"required downgrade", TLSRequired, "ERR FEATURE-NOT-CONFIGURED", "example.com", ca, true},
		{"optional unsupported", TLSOpportunistic, "ERR FEATURE-NOT-SUPPORTED", "example.com", ca, false},
		{"optional malformed", TLSOpportunistic, "OK NOT-STARTTLS", "example.com", ca, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			done := make(chan string, 1)
			var plaintextList atomic.Bool
			dial := func(context.Context, string, string) (net.Conn, error) {
				c, s := net.Pipe()
				go func() {
					defer func() { _ = s.Close() }()
					_ = s.SetDeadline(time.Now().Add(time.Second))
					line, err := bufio.NewReader(s).ReadString('\n')
					if err != nil {
						done <- ""
						return
					}
					if line != "STARTTLS\n" {
						done <- line
						return
					}
					if _, err = fmt.Fprintln(s, tc.reply); err != nil {
						done <- ""
						return
					}
					conn := s
					if tc.reply == "OK STARTTLS" {
						secure := tls.Server(s, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
						if err = secure.Handshake(); err != nil {
							done <- ""
							return
						}
						conn = secure
					}
					line, err = bufio.NewReader(conn).ReadString('\n')
					if err == nil && line == "LIST VAR rack-a\n" {
						_, _ = io.WriteString(conn, "BEGIN LIST VAR rack-a\nVAR rack-a ups.status \"OL\"\nEND LIST VAR rack-a\n")
					}
					done <- line
				}()
				return &tlsObservedConn{Conn: c, plaintextList: &plaintextList}, nil
			}
			client := NewClient(ClientOptions{DialContext: dial, Timeout: time.Second})
			vars, err := client.ListVariables(context.Background(), Target{Host: "127.0.0.1", UPSName: "rack-a", TLS: TLSOptions{Mode: tc.mode, CABundle: tc.trust, ServerName: tc.identity}})
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected TLS rejection")
				}
				if tc.reply == "OK STARTTLS" && tc.name != "bad CA" && !strings.Contains(err.Error(), "NUT TLS handshake") {
					t.Fatalf("expected TLS verification failure, got %v", err)
				}
			} else if err != nil || vars["ups.status"] != "OL" {
				t.Fatalf("variables=%v error=%v", vars, err)
			}
			wantPlaintext := !tc.wantErr && tc.reply == "ERR FEATURE-NOT-SUPPORTED"
			if plaintextList.Load() != wantPlaintext {
				t.Fatalf("plaintext LIST VAR attempted=%v, want %v", plaintextList.Load(), wantPlaintext)
			}
			select {
			case line := <-done:
				if tc.wantErr && strings.HasPrefix(line, "LIST VAR") {
					t.Fatalf("sent telemetry request after rejected TLS: %q", line)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("fixture did not exit")
			}
		})
	}
}

type tlsObservedConn struct {
	net.Conn
	plaintextList *atomic.Bool
}

func (c *tlsObservedConn) Write(data []byte) (int, error) {
	if strings.HasPrefix(string(data), "LIST VAR ") {
		c.plaintextList.Store(true)
	}
	return c.Conn.Write(data)
}

// The existing image smoke job supplies its isolated endpoint and ephemeral CA.
func TestClientTLSImage(t *testing.T) {
	address := os.Getenv("NUT_TLS_TEST_ADDRESS")
	if address == "" {
		t.Skip("requires the NUT TLS image smoke fixture")
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := os.ReadFile(os.Getenv("NUT_TLS_TEST_CA"))
	if err != nil {
		t.Fatal(err)
	}
	target := Target{
		Host: host, Port: port, UPSName: "smokeups",
		TLS: TLSOptions{Mode: TLSRequired, CABundle: ca, ServerName: "nut-server"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		variables, err := NewClient(ClientOptions{}).ListVariables(ctx, target)
		if err == nil {
			if variables["ups.status"] == "OL" {
				return
			}
			if variables["ups.status"] != "WAIT" {
				t.Fatalf("unexpected image telemetry: %v", variables)
			}
			// upsd may accept TLS before the replacement dummy driver connects.
		} else if !strings.Contains(err.Error(), "ERR DRIVER-NOT-CONNECTED") && !strings.Contains(err.Error(), "ERR DATA-STALE") {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("driver did not become ready: %v", err)
		case <-ticker.C:
		}
	}
}

func TestSTARTTLSCancellation(t *testing.T) {
	for _, phase := range []string{"reply", "handshake"} {
		t.Run(phase, func(t *testing.T) {
			ready := make(chan struct{})
			done := make(chan struct{})
			client := NewClient(ClientOptions{Timeout: time.Minute, DialContext: func(context.Context, string, string) (net.Conn, error) {
				c, s := net.Pipe()
				go func() {
					defer close(done)
					defer func() { _ = s.Close() }()
					_, _ = bufio.NewReader(s).ReadString('\n')
					if phase == "handshake" {
						_, _ = io.WriteString(s, "OK STARTTLS\n")
					}
					close(ready)
					_, _ = io.Copy(io.Discard, s)
				}()
				return c, nil
			}})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				_, err := client.ListVariables(ctx, Target{Host: "example.com", UPSName: "rack-a", TLS: TLSOptions{Mode: TLSRequired}})
				result <- err
			}()
			<-ready
			cancel()
			select {
			case err := <-result:
				if err == nil {
					t.Fatal("canceled poll succeeded")
				}
			case <-time.After(time.Second):
				t.Fatal("cancellation did not interrupt transport")
			}
			<-done
		})
	}
}

func TestSTARTTLSHandshakeDeadline(t *testing.T) {
	done := make(chan struct{})
	client := NewClient(ClientOptions{Timeout: 50 * time.Millisecond, DialContext: func(context.Context, string, string) (net.Conn, error) {
		c, s := net.Pipe()
		go func() {
			defer close(done)
			defer func() { _ = s.Close() }()
			_, _ = bufio.NewReader(s).ReadString('\n')
			_, _ = io.WriteString(s, "OK STARTTLS\n")
			_, _ = io.Copy(io.Discard, s)
		}()
		return c, nil
	}})
	result := make(chan error, 1)
	go func() {
		_, err := client.ListVariables(context.Background(), Target{Host: "example.com", UPSName: "rack-a", TLS: TLSOptions{Mode: TLSRequired}})
		result <- err
	}()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("stalled handshake succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("handshake ignored poll timeout")
	}
	<-done
}
