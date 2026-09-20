// Package command runs bounded host tools without shell interpolation or ambient
// Kubernetes configuration. It returns separate, capped streams for diagnostics.
package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"time"
)

type Spec struct {
	Name        string
	Args        []string
	Dir         string
	Env         map[string]string // Explicit child environment; nothing inherited automatically.
	Stdin       io.Reader
	Timeout     time.Duration
	OutputLimit int // Per stream; zero defaults to one MiB.
}

type Result struct {
	Stdout, Stderr                   string
	StdoutTruncated, StderrTruncated bool
}

// Run does not embed arguments, stdin, or output in its error: callers decide
// which retained diagnostics are safe to publish. Context errors remain unwrap-able.
// Linux cancellation kills the owned process group, including inherited-pipe children.
func Run(ctx context.Context, spec Spec) (Result, error) {
	if spec.Name == "" || spec.Timeout <= 0 || spec.OutputLimit < 0 {
		return Result{}, fmt.Errorf("command requires a name, positive timeout, and nonnegative output limit")
	}
	ctx, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	cmd := exec.CommandContext(ctx, spec.Name, spec.Args...)
	cmd.Dir, cmd.Stdin = spec.Dir, spec.Stdin
	cmd.Env = []string{}
	keys := make([]string, 0, len(spec.Env))
	for key := range spec.Env {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(spec.Env[key], 0) {
			return Result{}, fmt.Errorf("invalid command environment entry")
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		cmd.Env = append(cmd.Env, key+"="+spec.Env[key])
	}
	if err := configureCancellation(cmd); err != nil {
		return Result{}, err
	}
	limit := spec.OutputLimit
	if limit == 0 {
		limit = 1 << 20
	}
	stdout, stderr := &cappedBuffer{limit: limit}, &cappedBuffer{limit: limit}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	if errors.Is(err, exec.ErrWaitDelay) {
		// A tool exited but left descendants holding its output pipes open.
		// Finish cancelling that still-live process group before returning.
		err = errors.Join(err, cmd.Cancel())
	}
	result := Result{stdout.String(), stderr.String(), stdout.truncated, stderr.truncated}
	if err != nil {
		return result, errors.Join(ctx.Err(), fmt.Errorf("command failed: %w", err))
	}
	return result, ctx.Err()
}

type cappedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *cappedBuffer) Write(data []byte) (int, error) {
	n := len(data)
	remaining := b.limit - b.buf.Len()
	if len(data) > remaining {
		data = data[:remaining]
		b.truncated = true
	}
	_, _ = b.buf.Write(data)
	return n, nil // Keep draining even after the diagnostic limit.
}

func (b *cappedBuffer) String() string { return b.buf.String() }
