package nutreadiness

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const header = "UPSNAME\tUPSDRV\tRUNNING\tPF_PID\tS_RESPONSIVE\tS_PID\tS_STATUS\n"

func TestStatusClassification(t *testing.T) {
	for _, tc := range []struct {
		row   string
		ready bool
	}{
		{"ups\tdummy-ups\tN/A\t-3\tRESPONSIVE\t123\t\"OL\"\n", true},
		{"ups\tdummy-ups\tRUNNING\t123\tNOT_RESPONSIVE\tN/A\t\n", false},
		{"other\tdummy-ups\tN/A\t-3\tRESPONSIVE\t123\t\"OL\"\n", false},
		{"ups\tdummy-ups\tRUNNING\t123\tRESPONSIVE\n", false},
		{"ups\tdummy-ups\tRUNNING\t123\tRESPONSIVE\tN/A\t\n", false},
		{"ups\tdummy-ups\tRUNNING\t123\tRESPONSIVE\t0\t\n", false},
		{"SETINFO ups.status \"OL\"\n", false},
	} {
		if got := responsive([]byte(header+tc.row), "ups"); got != tc.ready {
			t.Errorf("%q: ready=%v", tc.row, got)
		}
	}
}

func TestEnumeration(t *testing.T) {
	got, err := driverNames([]byte("first\nlast\nfirst\n"))
	if err != nil || !slices.Equal(got, []string{"first", "last"}) {
		t.Fatalf("%v, %v", got, err)
	}
	for _, output := range []string{"", "\n", "tab\tname\n", "first\n\nlast", "nul\x00"} {
		if _, err := driverNames([]byte(output)); err == nil {
			t.Errorf("accepted %q", output)
		}
	}
	var out strings.Builder
	for i := range MaxDrivers {
		fmt.Fprintf(&out, "ups%d\n", i)
	}
	if names, err := driverNames([]byte(out.String())); err != nil || len(names) != MaxDrivers {
		t.Fatalf("maximum inventory: %v, %v", names, err)
	}
	fmt.Fprintln(&out, "one-too-many")
	if _, err := driverNames([]byte(out.String())); err == nil {
		t.Fatal("oversized inventory accepted")
	}
}

func helper(mode string) commandFunc {
	return func(ctx context.Context, args ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, os.Args[0], append([]string{"-test.run=^TestNUTCommand$", "--"}, args...)...)
		cmd.Env = append(os.Environ(), "NUT_READY_TEST="+mode, "GORACE=atexit_sleep_ms=0")
		return cmd
	}
}

// These real child processes model CLI results. Socket behavior is checked with
// the pinned NUT binary by TestRealNUTReadiness, not by this helper.
func TestNUTCommand(t *testing.T) {
	mode := os.Getenv("NUT_READY_TEST")
	if mode == "" {
		return
	}
	if os.Getenv("NUT_QUIET_INIT_BANNER") != "true" {
		os.Exit(3)
	}
	if mode == "environment" && os.Getenv("NUT_CONFPATH") != "/test/config" {
		os.Exit(3)
	}
	args := os.Args[slices.Index(os.Args, "--")+1:]
	if len(args) < 2 || args[0] != "--" {
		os.Exit(2)
	}
	args = args[1:]
	if args[0] == "list" {
		switch mode {
		case "enumeration blocked":
			time.Sleep(10 * time.Second)
		case "enumeration failed":
			os.Exit(1)
		case "empty":
			os.Exit(0)
		case "oversized output":
			fmt.Print(strings.Repeat("x", maxOutputBytes+1))
			os.Exit(0)
		case "oversized inventory":
			for i := range MaxDrivers + 1 {
				fmt.Printf("ups%d\n", i)
			}
			os.Exit(0)
		case "healthy first":
			fmt.Print("healthy\nfrozen\n")
			os.Exit(0)
		case "healthy last":
			fmt.Print("frozen\nhealthy\n")
			os.Exit(0)
		case "all frozen":
			fmt.Print("frozen\nfrozen2\nfrozen3\n")
			os.Exit(0)
		default:
			fmt.Print("healthy\n")
			os.Exit(0)
		}
	}
	if args[0] != "status" || len(args) != 2 {
		os.Exit(2)
	}
	if strings.HasPrefix(args[1], "frozen") || mode == "delayed" {
		time.Sleep(10 * time.Second)
	}
	if mode == "status failed" {
		os.Exit(1)
	}
	if mode == "stderr overflow" {
		fmt.Fprint(os.Stderr, strings.Repeat("x", maxOutputBytes+1))
	}
	if mode == "stdout overflow" {
		fmt.Print(strings.Repeat("x", maxOutputBytes+1))
	}
	if mode == "absent" {
		fmt.Print(header + "healthy\tdummy-ups\tN/A\t-3\tNOT_RESPONSIVE\tN/A\t\n")
		os.Exit(0)
	}
	fmt.Print(header + "healthy\tdummy-ups\tN/A\t-3\tRESPONSIVE\t123\t\"OL\"\n")
	os.Exit(0)
}

func TestCommandFailures(t *testing.T) {
	for _, mode := range []string{"enumeration failed", "empty", "oversized output", "status failed", "stderr overflow", "stdout overflow", "absent"} {
		t.Run(mode, func(t *testing.T) {
			if err := check(context.Background(), helper(mode)); err == nil {
				t.Fatal("failure passed readiness")
			}
		})
	}
}

func TestMixedOrder(t *testing.T) {
	for _, mode := range []string{"healthy first", "healthy last"} {
		t.Run(mode, func(t *testing.T) {
			start := time.Now()
			if err := check(context.Background(), helper(mode)); err != nil {
				t.Fatal(err)
			}
			if elapsed := time.Since(start); elapsed >= 2*time.Second {
				t.Fatalf("healthy driver waited for frozen driver: %s", elapsed)
			}
		})
	}
}

func TestCommandEnvironment(t *testing.T) {
	t.Setenv("NUT_CONFPATH", "/test/config")
	t.Setenv("NUT_QUIET_INIT_BANNER", "false")
	if err := check(context.Background(), helper("environment")); err != nil {
		t.Fatal(err)
	}
}

func TestOverflowStartsNoStatusCommands(t *testing.T) {
	statuses := 0
	command := func(ctx context.Context, args ...string) *exec.Cmd {
		if slices.Contains(args, "status") {
			statuses++
		}
		return helper("oversized inventory")(ctx, args...)
	}
	if err := check(context.Background(), command); err == nil {
		t.Fatal("oversized inventory passed")
	}
	if statuses != 0 {
		t.Fatalf("started %d status commands for oversized inventory", statuses)
	}
}

func TestCancellationReapsCommands(t *testing.T) {
	var mu sync.Mutex
	var commands []*exec.Cmd
	command := func(ctx context.Context, args ...string) *exec.Cmd {
		cmd := helper("all frozen")(ctx, args...)
		mu.Lock()
		commands = append(commands, cmd)
		mu.Unlock()
		return cmd
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := check(ctx, command); err == nil {
		t.Fatal("cancellation passed")
	}
	mu.Lock()
	defer mu.Unlock()
	for _, cmd := range commands {
		if cmd.Process != nil && cmd.ProcessState == nil {
			t.Fatalf("child %d was not waited for", cmd.Process.Pid)
		}
	}
}

func TestHyphenatedNamesAndOptionTerminator(t *testing.T) {
	for _, name := range []string{"rack-ups", "-rack-ups", "rack ups"} {
		if names, err := driverNames([]byte(name + "\n")); err != nil || len(names) != 1 || names[0] != name {
			t.Fatalf("name %q: %v, %v", name, names, err)
		}
		if !responsive([]byte(name+"\tdummy-ups\tN/A\t-3\tRESPONSIVE\t123\t\"OL\"\n"), name) {
			t.Fatalf("status name %q rejected", name)
		}
	}
}

func TestGlobalDeadlineAndRecovery(t *testing.T) {
	start := time.Now()
	if err := check(context.Background(), helper("all frozen")); err == nil {
		t.Fatal("frozen drivers passed")
	}
	if elapsed := time.Since(start); elapsed < Timeout || elapsed >= 5*time.Second {
		t.Fatalf("global deadline took %s", elapsed)
	}
	if err := check(context.Background(), helper("healthy")); err != nil {
		t.Fatalf("recovery: %v", err)
	}
}

func TestDeadlineCoversEnumerationAndDelayedReply(t *testing.T) {
	for _, mode := range []string{"enumeration blocked", "delayed"} {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		start := time.Now()
		if err := check(ctx, helper(mode)); err == nil {
			t.Fatal("timeout passed")
		}
		cancel()
		if time.Since(start) >= time.Second {
			t.Fatal("parent deadline not honored")
		}
	}
}
