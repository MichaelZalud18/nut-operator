//go:build hadron

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
	"net"
	"path/filepath"
	"testing"
	"time"
)

// fakeQMPServer listens on a unix socket and hands each accepted connection to handle, so each
// test can script exactly the greeting/capabilities/event sequence it wants to exercise, without a
// real QEMU process. It accepts once and stops -- every test below opens exactly one connection.
func fakeQMPServer(t *testing.T, handle func(conn net.Conn)) string {
	t.Helper()
	socketPath := filepath.Join(t.TempDir(), "qemu-qmp.sock")
	l, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listening on fake QMP socket: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		handle(conn)
	}()
	return socketPath
}

func TestWaitForQMPShutdownSucceedsOnRealEvent(t *testing.T) {
	socketPath := fakeQMPServer(t, func(conn net.Conn) {
		_, _ = conn.Write([]byte(`{"QMP":{"version":{}}}` + "\n"))
		buf := make([]byte, 4096)
		_, _ = conn.Read(buf) // qmp_capabilities request; this fake doesn't need to parse it.
		_, _ = conn.Write([]byte(`{"return":{}}` + "\n"))
		_, _ = conn.Write([]byte(`{"event":"SOME-OTHER-EVENT"}` + "\n")) // must not satisfy the wait
		_, _ = conn.Write([]byte(`{"event":"SHUTDOWN","data":{"guest":true}}` + "\n"))
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := waitForQMPShutdown(ctx, socketPath); err != nil {
		t.Fatalf("waitForQMPShutdown: %v", err)
	}
}

func TestWaitForQMPShutdownFailsClosedWhenSocketClosesWithoutShutdown(t *testing.T) {
	// Models an external kill: the connection just ends, with no SHUTDOWN event ever emitted --
	// this is exactly the false-pass case VM-9 requires this evidence to reject.
	socketPath := fakeQMPServer(t, func(conn net.Conn) {
		_, _ = conn.Write([]byte(`{"QMP":{"version":{}}}` + "\n"))
		buf := make([]byte, 4096)
		_, _ = conn.Read(buf)
		_, _ = conn.Write([]byte(`{"return":{}}` + "\n"))
		// Connection closes here (deferred in fakeQMPServer) without ever sending SHUTDOWN.
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := waitForQMPShutdown(ctx, socketPath)
	if err == nil {
		t.Fatal("waitForQMPShutdown: expected an error when the socket closes without a SHUTDOWN event, got nil")
	}
}

func TestWaitForQMPShutdownRejectsBadGreeting(t *testing.T) {
	socketPath := fakeQMPServer(t, func(conn net.Conn) {
		_, _ = conn.Write([]byte(`{"not-qmp":true}` + "\n"))
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := waitForQMPShutdown(ctx, socketPath); err == nil {
		t.Fatal("waitForQMPShutdown: expected an error for a non-QMP greeting, got nil")
	}
}

func TestWaitForQMPShutdownRejectsCapabilitiesError(t *testing.T) {
	socketPath := fakeQMPServer(t, func(conn net.Conn) {
		_, _ = conn.Write([]byte(`{"QMP":{"version":{}}}` + "\n"))
		buf := make([]byte, 4096)
		_, _ = conn.Read(buf)
		_, _ = conn.Write([]byte(`{"error":{"class":"GenericError","desc":"rejected"}}` + "\n"))
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := waitForQMPShutdown(ctx, socketPath)
	if err == nil {
		t.Fatal("waitForQMPShutdown: expected an error when capabilities negotiation is rejected, got nil")
	}
}

func TestWaitForQMPShutdownRespectsContextCancellation(t *testing.T) {
	// The fake server accepts but never writes anything -- waitForQMPShutdown must still return
	// promptly once ctx is done, not hang forever on the blocked read.
	socketPath := fakeQMPServer(t, func(conn net.Conn) {
		<-time.After(2 * time.Second)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := waitForQMPShutdown(ctx, socketPath)
	if err == nil {
		t.Fatal("waitForQMPShutdown: expected an error on context cancellation, got nil")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("waitForQMPShutdown: took %s to return after a 100ms context deadline", elapsed)
	}
}

func TestQMPSocketPathRequiresStateDir(t *testing.T) {
	if _, err := qmpSocketPath(nil); err == nil {
		t.Fatal("qmpSocketPath(nil): expected an error, got nil")
	}
}
