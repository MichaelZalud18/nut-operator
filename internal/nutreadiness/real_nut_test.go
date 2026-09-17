package nutreadiness

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func realNUT(t *testing.T, names ...string) string {
	t.Helper()
	if os.Getenv("NUT_READINESS_REAL") != "1" {
		t.Skip("set NUT_READINESS_REAL=1 in the operand image to test the real NUT CLI")
	}
	if _, err := exec.LookPath("upsdrvctl"); err != nil {
		t.Fatalf("real NUT requested: %v", err)
	}
	dir, err := os.MkdirTemp("", "nut-ready-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("NUT_CONFPATH", dir)
	t.Setenv("NUT_STATEPATH", dir)
	t.Setenv("NUT_ALTPIDPATH", dir)
	var config strings.Builder
	for _, name := range names {
		fmt.Fprintf(&config, "[%s]\n driver = dummy-ups\n port = fixture\n", name)
	}
	if err := os.WriteFile(filepath.Join(dir, "ups.conf"), []byte(config.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Only test code implements the socket protocol. The production wrapper always
// delegates driver selection and socket exchanges to the shipped upsdrvctl.
func driverSocket(t *testing.T, dir, driver, name string, mode *atomic.Value) {
	t.Helper()
	listener, err := net.Listen("unix", filepath.Join(dir, driver+"-"+name))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var clients sync.WaitGroup
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			clients.Go(func() {
				defer func() { _ = conn.Close() }()
				stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
				defer stop()
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				scan := bufio.NewScanner(conn)
				for scan.Scan() {
					behavior := mode.Load().(string)
					if behavior == "frozen" {
						continue
					}
					if !reply(ctx, conn, scan.Text(), behavior) {
						return
					}
				}
			})
		}
	}()
	t.Cleanup(func() { cancel(); _ = listener.Close(); <-done; clients.Wait() })
}

func reply(ctx context.Context, conn net.Conn, command, mode string) bool {
	response := ""
	switch command {
	case "NOBROADCAST":
		return true
	case "PING":
		switch mode {
		case "missing pong":
			return true
		case "partial pong":
			response = "PO"
		case "extra pong fields":
			response = "PONG extra\n"
		case "long noise then pong":
			response = strings.Repeat("x", 8<<10) + "\nPONG\n"
		case "fragmented pong", "malformed prefix":
			prefix := "PO"
			response = "NG\n"
			if mode == "malformed prefix" {
				prefix, response = "NOT", "PONG\n"
			}
			if _, err := io.WriteString(conn, prefix); err != nil {
				return false
			}
			select {
			case <-ctx.Done():
				return false
			case <-time.After(30 * time.Millisecond):
			}
		case "delayed pong":
			select {
			case <-ctx.Done():
				return false
			case <-time.After(4500 * time.Millisecond):
			}
			response = "PONG\n"
		default:
			response = "PONG\n"
		}
	case "GETPID":
		if mode == "missing pid" {
			return true
		}
		response = fmt.Sprintf("PID %d\n", os.Getpid())
	case "DUMPSTATUS":
		response = "SETINFO ups.status \"OL\"\nDATAOK\nDUMPDONE\n"
	case "LOGOUT":
		return false
	default:
		return false
	}
	_, err := io.WriteString(conn, response)
	return err == nil
}

func behavior(value string) *atomic.Value {
	v := new(atomic.Value)
	v.Store(value)
	return v
}

func TestRealNUTReadiness(t *testing.T) {
	for _, mode := range []string{"healthy", "absent", "frozen", "partial pong", "delayed pong", "missing pid"} {
		t.Run(mode, func(t *testing.T) {
			dir := realNUT(t, "test-ups")
			if mode != "absent" {
				driverSocket(t, dir, "dummy-ups", "test-ups", behavior(mode))
			}
			start := time.Now()
			err := Check(context.Background())
			if (err == nil) != (mode == "healthy") {
				t.Fatalf("mode %s: %v", mode, err)
			}
			if elapsed := time.Since(start); elapsed >= 5*time.Second {
				t.Fatalf("probe exceeded global bound: %s", elapsed)
			}
		})
	}
}

func TestRealNUTRejectsMissingPONG(t *testing.T) {
	dir := realNUT(t, "test-ups")
	driverSocket(t, dir, "dummy-ups", "test-ups", behavior("missing pong"))
	// Let the upstream three-second PING timeout and one-second close complete.
	// Unpatched NUT misclassifies this responsive GETPID/DUMPSTATUS-only fixture.
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	output, err := invoke(ctx, func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "upsdrvctl", args...)
	}, "--", "status", "test-ups")
	if err != nil {
		t.Fatalf("classification command did not complete: %v", err)
	}
	if responsive(output, "test-ups") {
		t.Fatalf("driver never answered PING but NUT marked it responsive: %s", output)
	}
	if !strings.Contains(string(output), "NOT_RESPONSIVE") {
		t.Fatalf("missing negative classification: %s", output)
	}
}

func TestRealNUTPONGFraming(t *testing.T) {
	for _, tc := range []struct {
		mode  string
		ready bool
	}{
		{mode: "fragmented pong", ready: true},
		{mode: "malformed prefix", ready: false},
		{mode: "extra pong fields", ready: false},
		{mode: "long noise then pong", ready: true},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			dir := realNUT(t, "test-ups")
			driverSocket(t, dir, "dummy-ups", "test-ups", behavior(tc.mode))
			start := time.Now()
			err := Check(context.Background())
			if (err == nil) != tc.ready {
				t.Fatalf("mode %s: ready=%v, error=%v", tc.mode, tc.ready, err)
			}
			if elapsed := time.Since(start); elapsed >= 5*time.Second {
				t.Fatalf("probe exceeded global bound: %s", elapsed)
			}
		})
	}
}

func TestRealNUTMixedOrderAndRecovery(t *testing.T) {
	for _, names := range [][]string{{"frozen", "healthy"}, {"healthy", "frozen"}} {
		t.Run(strings.Join(names, "-"), func(t *testing.T) {
			dir := realNUT(t, names...)
			driverSocket(t, dir, "dummy-ups", "frozen", behavior("frozen"))
			driverSocket(t, dir, "dummy-ups", "healthy", behavior("healthy"))
			if err := Check(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("recovery", func(t *testing.T) {
		dir := realNUT(t, "recovering")
		mode := behavior("frozen")
		driverSocket(t, dir, "dummy-ups", "recovering", mode)
		if err := Check(context.Background()); err == nil {
			t.Fatal("frozen driver passed")
		}
		mode.Store("healthy")
		if err := Check(context.Background()); err != nil {
			t.Fatalf("same socket recovery: %v", err)
		}
	})
}

func TestRealNUTIgnoresRetiredDriverSocket(t *testing.T) {
	dir := realNUT(t, "test-ups")
	driverSocket(t, dir, "retired-driver", "test-ups", behavior("healthy"))
	if err := Check(context.Background()); err == nil {
		t.Fatal("retired driver socket satisfied current configuration")
	}
}
