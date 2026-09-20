package diagnostics

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBoundedPrivateArtifactsSurviveCollectorFailure(t *testing.T) {
	parent := t.TempDir()
	failure := errors.New("private failure text")
	bundle, err := Capture(context.Background(), Options{Parent: parent, Timeout: time.Second, MaxBytes: 4}, []Collector{
		{Name: "large", Timeout: time.Second, Collect: func(_ context.Context, w io.Writer) error { _, _ = io.WriteString(w, "too much output"); return nil }},
		{Name: "failed", Timeout: time.Second, Collect: func(_ context.Context, w io.Writer) error { _, _ = io.WriteString(w, "safe"); return failure }},
		{Name: "after", Timeout: time.Second, Collect: func(_ context.Context, w io.Writer) error { _, err := io.WriteString(w, "last"); return err }},
	})
	if !errors.Is(err, ErrLimit) || !errors.Is(err, failure) || len(bundle.Entries) != 3 {
		t.Fatalf("%+v %v", bundle, err)
	}
	if !bundle.Entries[0].Truncated || bundle.Entries[0].Bytes != 4 {
		t.Fatal(bundle.Entries)
	}
	for i, want := range []string{"too ", "safe", "last"} {
		got, err := os.ReadFile(bundle.Entries[i].Path)
		if err != nil || string(got) != want {
			t.Fatalf("%q %v", got, err)
		}
		st, err := os.Stat(bundle.Entries[i].Path)
		if err != nil || st.Mode().Perm() != 0600 {
			t.Fatalf("file permissions: %v %v", st, err)
		}
	}
	st, err := os.Stat(bundle.Directory)
	if err != nil || st.Mode().Perm() != 0700 {
		t.Fatalf("directory permissions: %v %v", st, err)
	}
}

func TestBudgetsAndInvalidPlan(t *testing.T) {
	for _, total := range []bool{false, true} {
		t.Run(map[bool]string{false: "per-collector", true: "total"}[total], func(t *testing.T) {
			opts := Options{Parent: t.TempDir(), Timeout: time.Second, MaxBytes: 10}
			later := false
			collectors := []Collector{{Name: "wait", Timeout: 10 * time.Millisecond, Collect: func(ctx context.Context, _ io.Writer) error { <-ctx.Done(); return nil }}, {Name: "later", Timeout: time.Second, Collect: func(context.Context, io.Writer) error { later = true; return nil }}}
			if total {
				opts.Timeout = 10 * time.Millisecond
				collectors[0].Timeout = time.Second
			}
			_, err := Capture(context.Background(), opts, collectors)
			if !errors.Is(err, context.DeadlineExceeded) || later == total {
				t.Fatalf("budget error=%v later=%t", err, later)
			}
		})
	}
	parent := t.TempDir()
	for _, name := range []string{"../outside", "a/b", "", strings.Repeat("a", 65)} {
		_, err := Capture(context.Background(), Options{Parent: parent, Timeout: time.Second, MaxBytes: 1}, []Collector{{Name: name, Timeout: time.Second, Collect: func(context.Context, io.Writer) error { t.Fatal("invalid collector ran"); return nil }}})
		if err == nil {
			t.Fatal("invalid name accepted")
		}
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatalf("invalid plan created artifacts: %v %v", entries, err)
	}
}

func TestFreshDirectoriesAndPreCancelledCapture(t *testing.T) {
	parent := t.TempDir()
	existing := filepath.Join(parent, "keep")
	if err := os.WriteFile(existing, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	collectors := []Collector{{Name: "empty", Timeout: time.Second, Collect: func(context.Context, io.Writer) error { return nil }}}
	opts := Options{Parent: parent, Timeout: time.Second, MaxBytes: 1}
	first, err := Capture(context.Background(), opts, collectors)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Capture(context.Background(), opts, collectors)
	if err != nil || first.Directory == second.Directory {
		t.Fatalf("isolation: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b, err := Capture(ctx, opts, collectors)
	if !errors.Is(err, context.Canceled) || b.Directory != "" {
		t.Fatalf("cancelled capture: %+v %v", b, err)
	}
	if data, err := os.ReadFile(existing); err != nil || string(data) != "keep" {
		t.Fatalf("caller data changed: %v", err)
	}
}
