package controller

import (
	"context"
	"fmt"
	"sync"
	"time"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

const maxConcurrentFlowRuns = 8

// flowRuns owns execution contexts and immutable status snapshots. Only Reconcile
// writes Kubernetes status; completed snapshots stay owned until that write succeeds.
type flowRuns struct {
	mu     sync.Mutex
	ctx    context.Context
	ready  chan struct{}
	limit  int
	runs   map[string]*flowRun
	claims map[string]string
	events chan event.GenericEvent
	wg     sync.WaitGroup
}

type flowRun struct {
	flow    *power.ShutdownFlow
	cancel  context.CancelFunc
	done    bool
	discard bool
	cadence time.Duration
}

func newFlowRuns(limit int) *flowRuns {
	return &flowRuns{limit: limit, ready: make(chan struct{}), runs: map[string]*flowRun{}, claims: map[string]string{}, events: make(chan event.GenericEvent, limit)}
}

func (s *flowRuns) NeedLeaderElection() bool { return true }

func (s *flowRuns) Start(ctx context.Context) error {
	s.mu.Lock()
	s.ctx = ctx
	close(s.ready)
	s.mu.Unlock()
	<-ctx.Done()
	// Admission and WaitGroup.Add share this lock; shutdown closes admission before Wait.
	s.mu.Lock()
	for _, run := range s.runs {
		run.cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
	return nil
}

func (s *flowRuns) submit(flow *power.ShutdownFlow, cadence time.Duration, work func(context.Context, *power.ShutdownFlow, func())) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx == nil || s.ctx.Err() != nil || len(s.runs) >= s.limit || s.runs[flow.Name] != nil {
		return false
	}
	ctx, cancel := context.WithCancel(s.ctx)
	run := &flowRun{flow: flow.DeepCopy(), cancel: cancel, cadence: cadence}
	s.runs[flow.Name] = run
	owned := flow.DeepCopy()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
		publish := func() {
			s.mu.Lock()
			run.flow = owned.DeepCopy()
			s.mu.Unlock()
			s.notify(owned.Name)
		}
		work(ctx, owned, publish)
		s.mu.Lock()
		run.flow, run.done = owned.DeepCopy(), true
		s.releaseLocked(owned.Name)
		if run.discard {
			delete(s.runs, owned.Name)
		}
		s.mu.Unlock()
		s.notify(owned.Name)
	}()
	return true
}

func (s *flowRuns) notify(name string) {
	flow := &power.ShutdownFlow{}
	flow.Name = name
	select {
	case s.events <- event.GenericEvent{Object: flow}:
	default: // The per-flow heartbeat remains a progress/completion backstop.
	}
}

func (s *flowRuns) snapshot(name string) *flowRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run := s.runs[name]; run != nil {
		return &flowRun{flow: run.flow.DeepCopy(), done: run.done, cadence: run.cadence}
	}
	return nil
}

func (s *flowRuns) cancel(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run := s.runs[name]; run != nil {
		run.cancel()
		run.discard = true
		if run.done {
			delete(s.runs, name)
		}
	}
}

func (s *flowRuns) acknowledge(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run := s.runs[name]; run != nil && run.done {
		delete(s.runs, name)
	}
}

// Claims are nonblocking and held for the entire execution. A losing flow fails
// before execution, rather than waiting while holding a partial set of resources.
func (s *flowRuns) claim(owner string, keys []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, key := range keys {
		for held, other := range s.claims {
			if other != owner && (key == "*" || held == "*" || held == key) {
				return fmt.Errorf("shutdown flow %q owns conflicting execution resources", other)
			}
		}
	}
	for _, key := range keys {
		s.claims[key] = owner
	}
	return nil
}

func (s *flowRuns) release(owner string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releaseLocked(owner)
}

func (s *flowRuns) releaseLocked(owner string) {
	for key, other := range s.claims {
		if owner == other {
			delete(s.claims, key)
		}
	}
}
