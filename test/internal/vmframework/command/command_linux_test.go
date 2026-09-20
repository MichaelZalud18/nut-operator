package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MichaelZalud18/nut-operator/test/internal/vmframework/readiness"
)

func TestCommandHelper(t *testing.T) {
	mode := os.Getenv("VM_COMMAND_HELPER")
	if mode == "" {
		return
	}
	switch mode {
	case "echo":
		_, _ = io.Copy(os.Stdout, os.Stdin)
		_, _ = fmt.Fprint(os.Stderr, os.Getenv("EXPLICIT"))
		_, _ = fmt.Fprint(os.Stdout, os.Getenv("AMBIENT_SECRET"))
	case "fail":
		_, _ = fmt.Fprint(os.Stderr, "sensitive diagnostic")
		os.Exit(3)
	case "large":
		_, _ = fmt.Fprint(os.Stdout, strings.Repeat("a", 100000))
		_, _ = fmt.Fprint(os.Stderr, strings.Repeat("b", 100000))
	case "block":
		time.Sleep(time.Minute)
	case "child", "orphan":
		child := exec.Command(os.Args[0], "-test.run=^TestCommandHelper$")
		child.Env = []string{"VM_COMMAND_HELPER=block", "GORACE=atexit_sleep_ms=0"}
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(4)
		}
		if err := os.WriteFile(os.Getenv("VM_CHILD_PID"), []byte(fmt.Sprint(child.Process.Pid)), 0600); err != nil {
			os.Exit(5)
		}
		if mode == "child" {
			time.Sleep(time.Minute)
		}
	}
	os.Exit(0)
}

func helper(t *testing.T, mode string) Spec {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Spec{Name: exe, Args: []string{"-test.run=^TestCommandHelper$"}, Timeout: 5 * time.Second,
		Env: map[string]string{"VM_COMMAND_HELPER": mode, "GORACE": "atexit_sleep_ms=0", "GOCOVERDIR": t.TempDir()}}
}

func TestExplicitEnvironmentStdinAndSeparateOutput(t *testing.T) {
	t.Setenv("AMBIENT_SECRET", "must-not-leak")
	spec := helper(t, "echo")
	spec.Env["EXPLICIT"] = "explicit stderr"
	spec.Stdin = strings.NewReader("literal $(no-shell) stdin")
	result, err := Run(context.Background(), spec)
	if err != nil || result.Stdout != "literal $(no-shell) stdin" || result.Stderr != "explicit stderr" {
		t.Fatalf("output separation/environment: %+v, %v", result, err)
	}
}

func TestFailureKeepsPrivateOutputOutOfError(t *testing.T) {
	result, err := Run(context.Background(), helper(t, "fail"))
	if err == nil || strings.Contains(err.Error(), "sensitive") || result.Stderr != "sensitive diagnostic" {
		t.Fatalf("unexpected error/output: %+v, %v", result, err)
	}
}

func TestOutputLimitsStillDrainBothStreams(t *testing.T) {
	spec := helper(t, "large")
	spec.OutputLimit = 32
	result, err := Run(context.Background(), spec)
	if err != nil || len(result.Stdout) != 32 || len(result.Stderr) != 32 || !result.StdoutTruncated || !result.StderrTruncated {
		t.Fatalf("output cap: %+v, %v", result, err)
	}
}

func TestTimeoutAndPreCancellation(t *testing.T) {
	spec := helper(t, "block")
	spec.Timeout = 30 * time.Millisecond
	if _, err := Run(context.Background(), spec); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Run(ctx, spec); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestScopedToolSpecs(t *testing.T) {
	spec, err := Kubectl("/private/run/kubeconfig", time.Second, strings.NewReader("manifest"), "apply", "-f", "-")
	if err != nil || spec.Args[0] != "--kubeconfig" || spec.Args[1] != "/private/run/kubeconfig" || spec.Stdin == nil {
		t.Fatalf("kubectl spec: %+v, %v", spec, err)
	}
	if _, err := Kubectl("", time.Second, nil); err == nil {
		t.Fatal("ambient kubeconfig accepted")
	}
	if _, err := Kubectl("/private/run/kubeconfig", time.Second, nil, "--server=https://foreign", "get", "nodes"); err == nil {
		t.Fatal("cluster override accepted")
	}
	if _, err := Make("relative", time.Second, nil, "deploy"); err == nil {
		t.Fatal("ambient work directory accepted")
	}
	if _, err := Run(context.Background(), Spec{Name: "unused"}); err == nil {
		t.Fatal("unbounded command accepted")
	}
	bad := helper(t, "echo")
	bad.Env["bad=key"] = "value"
	if _, err := Run(context.Background(), bad); err == nil {
		t.Fatal("invalid environment accepted")
	}
}

func TestCancellationStopsInheritedPipeChildren(t *testing.T) {
	for _, mode := range []string{"child", "orphan"} {
		t.Run(mode, func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "pid")
			spec := helper(t, mode)
			spec.Env["VM_CHILD_PID"] = pidFile
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() { _, err := Run(ctx, spec); result <- err }()
			var pid string
			opts := readiness.Options{Timeout: 2 * time.Second, Interval: time.Millisecond}
			if err := readiness.Wait(context.Background(), opts, func(context.Context) error {
				data, err := os.ReadFile(pidFile)
				pid = strings.TrimSpace(string(data))
				return err
			}, nil); err != nil {
				t.Fatal(err)
			}
			if mode == "child" {
				cancel()
			}
			if err := <-result; err == nil {
				t.Fatal("expected cancellation or inherited-pipe failure")
			}
			if err := readiness.Wait(context.Background(), opts, func(context.Context) error {
				data, err := os.ReadFile("/proc/" + pid + "/stat")
				if os.IsNotExist(err) {
					return nil
				}
				if err != nil {
					return err
				}
				rest := string(data)[strings.LastIndex(string(data), ")")+1:]
				if fields := strings.Fields(rest); len(fields) > 0 && fields[0] == "Z" {
					return nil
				}
				return fmt.Errorf("owned child still running")
			}, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRepositoryLookupUsesExplicitDirectory(t *testing.T) {
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := RepositoryRoot(context.Background(), directory)
	if err != nil || !filepath.IsAbs(root) {
		t.Fatalf("repository root: %q, %v", root, err)
	}
}
