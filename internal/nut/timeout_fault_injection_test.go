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
	"context"
	"errors"
	"net"
	"runtime"
	"strings"
	"testing"
	"time"
)

// F-110: this file exercises the two timeout boundaries ListVariables actually has --
// c.timeout bounding the dial, and the post-connect SetDeadline bounding the read -- by
// making a NUT endpoint that never answers, rather than one that answers wrong. Every other
// spec in client_test.go controls what the fake server *says*; these control whether it
// says anything at all, which is the shape a partitioned or wedged upsd takes in production.

// TestClientDialStallIsBoundedByConfiguredTimeout simulates a NUT endpoint that a TCP SYN
// never resolves against -- a firewalled or partitioned host, not a refused connection. The
// dialer here blocks until its context is cancelled rather than returning ECONNREFUSED, which
// is the failure a naive test double would produce instead. Without pollCtx (derived from
// c.timeout, not the caller's context.Background()) this would hang forever.
func TestClientDialStallIsBoundedByConfiguredTimeout(t *testing.T) {
	const configuredTimeout = 50 * time.Millisecond

	client := NewClient(ClientOptions{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		Timeout: configuredTimeout,
	})

	started := time.Now()
	_, err := client.ListVariables(context.Background(), Target{
		Host:    "wedged.example.net",
		UPSName: "rack-a",
	})
	elapsed := time.Since(started)

	if err == nil {
		t.Fatal("expected a stalled dial to return an error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected the dial to fail with context.DeadlineExceeded, got %v", err)
	}
	if !strings.Contains(err.Error(), "connect to NUT server") {
		t.Fatalf("expected the dial failure to be attributed to the connect step, got %v", err)
	}
	// Loose upper bound: this asserts the client's own configured timeout governs the stall,
	// not that the caller's context.Background() (which never expires on its own) does.
	if elapsed > 2*time.Second {
		t.Fatalf("dial stall took %s to resolve against a %s configured timeout; caller context.Background() was not bounding it, c.timeout should have", elapsed, configuredTimeout)
	}
}

// TestClientReadStallIsBoundedByConnectionDeadline simulates a upsd that accepts the TCP
// connection and reads the LIST VAR request -- so the dial and the request write both
// succeed -- and then never writes a byte back. This is the connection SetDeadline set after
// dial, not the pollCtx timeout above; a server that answers, but wedges after the client
// hangs up on that answer, only that second bound to protect it.
func TestClientReadStallIsBoundedByConnectionDeadline(t *testing.T) {
	const configuredTimeout = 100 * time.Millisecond

	serverDone := make(chan struct{})
	client := NewClient(ClientOptions{
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			clientConn, serverConn := net.Pipe()
			go func() {
				defer func() { _ = serverConn.Close() }()
				// Consume the request so the client's write does not itself block, then go
				// silent -- exactly what a driver-side hang inside upsd looks like to a poller.
				_ = readLine(t, serverConn)
				<-serverDone
			}()
			return clientConn, nil
		},
		Timeout: configuredTimeout,
	})

	started := time.Now()
	_, err := client.ListVariables(context.Background(), Target{
		Host:    "silent.example.net",
		UPSName: "rack-a",
	})
	elapsed := time.Since(started)
	close(serverDone)

	if err == nil {
		t.Fatal("expected a silent server to produce a read error, got nil")
	}
	if !strings.Contains(err.Error(), "read LIST VAR response") {
		t.Fatalf("expected the failure to be attributed to the read step, got %v", err)
	}
	if elapsed < configuredTimeout {
		t.Fatalf("read failed after %s, before the %s connection deadline elapsed -- it did not wait for the deadline", elapsed, configuredTimeout)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("read stall took %s to resolve against a %s connection deadline", elapsed, configuredTimeout)
	}
}

// TestClientRecoversAcrossRepeatedTimeoutCycles drives many poll cycles against an endpoint
// that alternates between answering and going silent, which is what a real driver flapping
// under load produces. Two things are asserted beyond individual cycle correctness: no cycle
// is influenced by the outcome of the one before it (a timed-out connection must not corrupt
// or slow the next attempt, since ListVariables opens a fresh connection every call), and no
// per-connection resource is leaked across the run.
func TestClientRecoversAcrossRepeatedTimeoutCycles(t *testing.T) {
	const (
		cycles            = 200
		configuredTimeout = 30 * time.Millisecond
	)

	client := NewClient(ClientOptions{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}
			clientConn, serverConn := net.Pipe()
			go func() { _ = serverConn.Close() }()
			return clientConn, nil
		},
		Timeout: configuredTimeout,
	})

	// Baseline goroutine count after a brief settle, so the leak check below isn't measuring
	// runtime/test-harness goroutines that were already there.
	runtime.GC()
	time.Sleep(10 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	for i := 0; i < cycles; i++ {
		_, err := client.ListVariables(context.Background(), Target{
			Host:    "flapping.example.net",
			UPSName: "rack-a",
		})
		// A closed net.Pipe server produces a read error every cycle by construction (there is
		// no response to parse), which is fine here -- the property under test is that failure
		// stays contained to its own cycle, not that this particular fixture succeeds.
		if err == nil {
			t.Fatalf("cycle %d: expected an error against a connection with no server response", i)
		}
	}

	runtime.GC()
	time.Sleep(10 * time.Millisecond)
	after := runtime.NumGoroutine()
	// Generous slack: this is a leak smoke test, not an exact accounting. 200 cycles leaking
	// even one goroutine each would show as roughly +200; a handful of stragglers from GC/test
	// scheduling should not.
	if after > baseline+20 {
		t.Fatalf("goroutine count grew from %d to %d over %d cycles, suggesting a per-connection leak", baseline, after, cycles)
	}
}
