//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Compile the production registration function into an isolated Ginkgo suite.
// Loading the e2e suite itself would also load its cluster-owning BeforeSuite.
func TestManagerLifecycleRegistration(t *testing.T) {
	source := managerLifecycleRegistrationSource(t)
	path := filepath.Join(t.TempDir(), "lifecycle_test.go")
	if err := os.WriteFile(path, []byte(managerLifecycleHarness+source), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, want string
		fails      bool
	}{
		{"success", "setup,spec-1,fixture-1,spec-2,fixture-2,undeploy,uninstall,namespace", false},
		{"failure", "setup,spec-1,diagnostics,fixture-1,undeploy,uninstall,namespace", true},
		{"partial-setup", "setup,diagnostics,undeploy,uninstall,namespace", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, "go", "test", "-count=1", "-v", path)
			cmd.Env = append(os.Environ(), "NUT_LIFECYCLE_SCENARIO="+tc.name)
			out, err := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("lifecycle subprocess timed out: %s", out)
			}
			if (err != nil) != tc.fails {
				t.Fatalf("unexpected suite result: %v\n%s", err, out)
			}
			if !strings.Contains(string(out), "LIFECYCLE:"+tc.want+"\n") {
				t.Fatalf("want lifecycle %s\n%s", tc.want, out)
			}
		})
	}
}

func TestManagerLifecycleRegistrationSourceAfterChdir(t *testing.T) {
	want := managerLifecycleRegistrationSource(t)
	t.Chdir(t.TempDir())
	if got := managerLifecycleRegistrationSource(t); got != want {
		t.Fatal("registration source changed with working directory")
	}
}

func managerLifecycleRegistrationSource(t *testing.T) string {
	t.Helper()
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate lifecycle registration test source")
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(filepath.Dir(sourcePath), "e2e_test.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "registerManagerLifecycle" {
			continue
		}
		var source bytes.Buffer
		if err := format.Node(&source, fset, fn); err != nil {
			t.Fatal(err)
		}
		return source.String()
	}
	t.Fatal("manager lifecycle registration function not found")
	return ""
}

const managerLifecycleHarness = `package lifecycle_test

import (
  "fmt"
  "os"
  "strings"
  "testing"
  . "github.com/onsi/ginkgo/v2"
)

func TestLifecycle(t *testing.T) {
  scenario := os.Getenv("NUT_LIFECYCLE_SCENARIO")
  var events []string
  Describe("Manager", Ordered, func() {
    registerManagerLifecycle(func() {
      events = append(events, "setup")
      if scenario == "partial-setup" { Fail("injected setup failure") }
    }, func() {
      events = append(events, "undeploy", "uninstall", "namespace")
    }, func() {
      if CurrentSpecReport().Failed() { events = append(events, "diagnostics") }
    })
    It("first fixture", func() {
      DeferCleanup(func() { events = append(events, "fixture-1") })
      events = append(events, "spec-1")
      if scenario == "failure" { Fail("injected spec failure") }
    })
    It("second fixture", func() {
      DeferCleanup(func() { events = append(events, "fixture-2") })
      events = append(events, "spec-2")
    })
  })
  config, reporter := GinkgoConfiguration()
  config.RandomSeed = 1
  config.FailFast = false
  config.FlakeAttempts = 0
  config.FocusStrings = nil
  config.SkipStrings = nil
  config.LabelFilter = ""
  reporter.NoColor = true
  RunSpecs(t, "manager lifecycle", config, reporter)
  fmt.Printf("LIFECYCLE:%s\n", strings.Join(events, ","))
}

`
