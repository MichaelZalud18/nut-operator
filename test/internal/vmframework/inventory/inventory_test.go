package inventory

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestTokenFingerprints(t *testing.T) {
	a, shapeA, _ := fingerprints([]byte("{ return first(value) }"))
	b, shapeB, _ := fingerprints([]byte("{ return second(other) }"))
	c, _, _ := fingerprints([]byte("{ /* comment */ return first(value) }"))
	if a == b || shapeA != shapeB || a != c {
		t.Fatal("exact/structural token comparison lost its contract")
	}
}

func TestScanIncludesBuildTaggedSourcesAndNonGoOwners(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"test/hadron/source.go", "test/talos/source.go", ".github/workflows/hadron-smoke.yml",
		".github/workflows/talos-smoke.yml", "hack/hadron-cleanup.py", "hack/talos-cleanup.py",
		"hack/test_hadron_cleanup.py", "hack/test_talos_cleanup.py", "hack/hadron-vm-probe.sh",
		"hack/webhook-cert.sh", "Makefile"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		data := []byte("fixture")
		if filepath.Ext(name) == ".go" {
			data = []byte("//go:build unavailable\n\npackage fixture\nfunc copied() int { return 42 }\n")
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	a, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Scan(root)
	if err != nil || !reflect.DeepEqual(a, b) || len(a.Files) != 11 || len(a.Duplicates) != 2 {
		t.Fatalf("unstable/incomplete inventory: %+v, %v", a, err)
	}
}

func TestMissingSourceIsNotSilentlySkipped(t *testing.T) {
	if _, err := Scan(t.TempDir()); err == nil {
		t.Fatal("empty inventory accepted")
	}
}
