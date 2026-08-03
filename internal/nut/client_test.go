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
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestClientListsVariables(t *testing.T) {
	client := NewClient(ClientOptions{
		DialContext: fakeNUTServer(t, func(t *testing.T, conn net.Conn) {
			request := readLine(t, conn)
			if request != "LIST VAR rack-a" {
				t.Fatalf("expected LIST VAR request, got %q", request)
			}
			writeResponse(t, conn, strings.Join([]string{
				"BEGIN LIST VAR rack-a",
				`VAR rack-a ups.status "OB LB"`,
				`VAR rack-a battery.charge "42.5"`,
				`VAR rack-a ups.model "Example \"quoted\" model"`,
				"END LIST VAR rack-a",
				"",
			}, "\n"))
		}),
		Timeout: time.Second,
	})

	variables, err := client.ListVariables(context.Background(), Target{
		Host:    "nut.example.net",
		UPSName: "rack-a",
	})
	if err != nil {
		t.Fatalf("ListVariables returned error: %v", err)
	}
	if variables["ups.status"] != "OB LB" {
		t.Fatalf("expected ups.status to be parsed, got %#v", variables)
	}
	if variables["battery.charge"] != "42.5" {
		t.Fatalf("expected battery.charge to be parsed, got %#v", variables)
	}
	if variables["ups.model"] != `Example "quoted" model` {
		t.Fatalf("expected escaped quote to be parsed, got %q", variables["ups.model"])
	}
}

func TestClientRejectsNUTErrors(t *testing.T) {
	client := NewClient(ClientOptions{
		DialContext: fakeNUTServer(t, func(t *testing.T, conn net.Conn) {
			_ = readLine(t, conn)
			writeResponse(t, conn, "ERR UNKNOWN-UPS\n")
		}),
		Timeout: time.Second,
	})

	_, err := client.ListVariables(context.Background(), Target{
		Host:    "nut.example.net",
		UPSName: "missing",
	})
	if err == nil || !strings.Contains(err.Error(), "ERR UNKNOWN-UPS") {
		t.Fatalf("expected NUT error to be surfaced, got %v", err)
	}
}

func TestClientRejectsMalformedResponses(t *testing.T) {
	tests := []struct {
		name     string
		response string
		contains string
	}{
		{
			name:     "missing begin",
			response: `VAR rack-a ups.status "OL"` + "\n",
			contains: "expected",
		},
		{
			name: "invalid variable line",
			response: strings.Join([]string{
				"BEGIN LIST VAR rack-a",
				`BROKEN rack-a ups.status "OL"`,
				"END LIST VAR rack-a",
				"",
			}, "\n"),
			contains: "invalid LIST VAR line",
		},
		{
			name: "unterminated list",
			response: strings.Join([]string{
				"BEGIN LIST VAR rack-a",
				`VAR rack-a ups.status "OL"`,
				"",
			}, "\n"),
			contains: "before end of stream",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(ClientOptions{
				DialContext: fakeNUTServer(t, func(t *testing.T, conn net.Conn) {
					_ = readLine(t, conn)
					writeResponse(t, conn, tt.response)
				}),
				Timeout: time.Second,
			})

			_, err := client.ListVariables(context.Background(), Target{
				Host:    "nut.example.net",
				UPSName: "rack-a",
			})
			if err == nil || !strings.Contains(err.Error(), tt.contains) {
				t.Fatalf("expected error containing %q, got %v", tt.contains, err)
			}
		})
	}
}

func TestClientValidatesTarget(t *testing.T) {
	client := NewClient(ClientOptions{
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			t.Fatal("dialer should not be called for invalid target")
			return nil, errors.New("unexpected dial")
		},
	})

	for _, target := range []Target{
		{UPSName: "rack-a"},
		{Host: "nut.example.net"},
		{Host: "nut.example.net", UPSName: "bad name"},
		{Host: "nut.example.net", UPSName: "rack-a", Port: 70000},
	} {
		if _, err := client.ListVariables(context.Background(), target); err == nil {
			t.Fatalf("expected target %#v to be rejected", target)
		}
	}
}

func fakeNUTServer(t *testing.T, serve func(*testing.T, net.Conn)) DialContext {
	t.Helper()
	return func(context.Context, string, string) (net.Conn, error) {
		clientConn, serverConn := net.Pipe()
		go func() {
			defer func() {
				_ = serverConn.Close()
			}()
			serve(t, serverConn)
		}()
		return clientConn, nil
	}
}

func readLine(t *testing.T, reader io.Reader) string {
	t.Helper()
	line, err := bufio.NewReader(reader).ReadString('\n')
	if err != nil {
		t.Fatalf("read request: %v", err)
	}
	return strings.TrimRight(line, "\r\n")
}

func writeResponse(t *testing.T, writer io.Writer, response string) {
	t.Helper()
	if _, err := io.WriteString(writer, response); err != nil {
		t.Fatalf("write response: %v", err)
	}
}
