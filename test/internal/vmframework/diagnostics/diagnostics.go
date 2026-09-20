// Package diagnostics retains bounded, explicitly selected diagnostic streams.
// Collectors are responsible for redaction; there is no automatic cluster dump.
package diagnostics

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

var ErrLimit = errors.New("diagnostic output limit reached")
var safeName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

type Collector struct {
	Name    string
	Timeout time.Duration
	// Collect must honor ctx and finish all writes before returning.
	Collect func(context.Context, io.Writer) error
}

type Options struct {
	Parent   string
	Timeout  time.Duration
	MaxBytes int64 // Per collector. Required; there is no unbounded mode.
}

type Entry struct {
	Name, Path string
	Bytes      int64
	Truncated  bool
	Err        error
}

type Bundle struct {
	Directory string
	Entries   []Entry
}

// Capture creates a private, unique directory and retains it even on failure.
// Collectors run sequentially, continue after independent failures, and stop when
// the total context expires. Each returned entry describes an attempted collector.
// It never deletes caller data or writes callback errors (which may contain secrets)
// to disk. Callers own retention and publishing policy.
func Capture(parent context.Context, opts Options, collectors []Collector) (Bundle, error) {
	if err := validate(opts, collectors); err != nil {
		return Bundle{}, err
	}
	ctx, cancel := context.WithTimeout(parent, opts.Timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return Bundle{}, err
	}
	dir, err := os.MkdirTemp(opts.Parent, "vm-diagnostics-")
	if err != nil {
		return Bundle{}, err
	}
	bundle := Bundle{Directory: dir}
	var failures []error
	for _, collector := range collectors {
		if err := ctx.Err(); err != nil {
			failures = append(failures, err)
			break
		}
		entry := capture(ctx, dir, opts.MaxBytes, collector)
		bundle.Entries = append(bundle.Entries, entry)
		if entry.Err != nil {
			failures = append(failures, fmt.Errorf("collector %s: %w", collector.Name, entry.Err))
		}
	}
	return bundle, errors.Join(append(failures, ctx.Err())...)
}

func capture(parent context.Context, dir string, limit int64, c Collector) Entry {
	entry := Entry{Name: c.Name, Path: filepath.Join(dir, c.Name+".log")}
	file, err := os.OpenFile(entry.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		entry.Err = err
		return entry
	}
	defer func() { _ = file.Close() }()
	writer := &limitedWriter{writer: file, remaining: limit}
	ctx, cancel := context.WithTimeout(parent, c.Timeout)
	defer cancel()
	collectErr := c.Collect(ctx, writer)
	entry.Err = errors.Join(collectErr, ctx.Err(), writer.err, file.Close())
	entry.Bytes = limit - writer.remaining
	entry.Truncated = writer.truncated
	if writer.truncated {
		entry.Err = errors.Join(entry.Err, ErrLimit)
	}
	return entry
}

type limitedWriter struct {
	writer    io.Writer
	remaining int64
	truncated bool
	err       error
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	overflow := int64(len(p)) > w.remaining
	if overflow {
		p = p[:w.remaining]
		w.truncated = true
	}
	n, err := w.writer.Write(p)
	w.remaining -= int64(n)
	if err != nil {
		w.err = errors.Join(w.err, err)
	}
	if overflow {
		err = errors.Join(err, ErrLimit)
	}
	return n, err
}

func validate(opts Options, collectors []Collector) error {
	if !filepath.IsAbs(opts.Parent) || opts.Timeout <= 0 || opts.MaxBytes <= 0 || len(collectors) == 0 {
		return fmt.Errorf("absolute parent, positive budgets and collectors required")
	}
	names := map[string]bool{}
	for _, c := range collectors {
		if !safeName.MatchString(c.Name) || names[c.Name] || c.Timeout <= 0 || c.Collect == nil {
			return fmt.Errorf("unique safe collector names, positive budgets and callbacks required")
		}
		names[c.Name] = true
	}
	return nil
}
