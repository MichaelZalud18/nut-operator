// Package lifecycle coordinates owned-resource cleanup independently of guest OS.
// Resource callbacks retain responsibility for identity checks and actual stopping.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

type resource struct {
	name             string
	stop, remove     func(context.Context) error
	stopped, removed bool
}

type Scope struct {
	mu        sync.Mutex
	resources []resource
	sealed    bool
}

// Add registers cleanup before startup so partial-start failures are covered.
// Stop must verify ownership and honor ctx; remove runs only after all stops
// succeed. Callbacks must not call methods on this scope recursively.
func (s *Scope) Add(name string, stop, remove func(context.Context) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sealed || name == "" || stop == nil {
		return fmt.Errorf("open scope, resource name, and owned stop callback required")
	}
	for _, existing := range s.resources {
		if existing.name == name {
			return fmt.Errorf("duplicate resource name")
		}
	}
	s.resources = append(s.resources, resource{name: name, stop: stop, remove: remove})
	return nil
}

// Finish stops in reverse registration order, attempting every stop even after
// failure. It gives cleanup its own total budget after parent cancellation.
// Failed stops preserve all state. preserve=true also retains state on success.
// Repeated calls retry failed operations without repeating completed ones.
// Callbacks must honor ctx; local filesystem operations are not forcibly interruptible.
func (s *Scope) Finish(parent context.Context, budget time.Duration, preserve bool) error {
	if budget <= 0 {
		return fmt.Errorf("positive cleanup budget required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sealed = true
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), budget)
	defer cancel()
	var failures []error
	for i := len(s.resources) - 1; i >= 0; i-- {
		r := &s.resources[i]
		if r.stopped {
			continue
		}
		if err := r.stop(ctx); err != nil {
			failures = append(failures, fmt.Errorf("stop %s: %w", r.name, err))
		} else {
			r.stopped = true
		}
	}
	if len(failures) > 0 || preserve || ctx.Err() != nil {
		return errors.Join(append(failures, ctx.Err())...)
	}
	for i := len(s.resources) - 1; i >= 0; i-- {
		r := &s.resources[i]
		if r.removed || r.remove == nil {
			continue
		}
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		if err := r.remove(ctx); err != nil {
			failures = append(failures, fmt.Errorf("remove %s: %w", r.name, err))
		} else {
			r.removed = true
		}
	}
	return errors.Join(failures...)
}
