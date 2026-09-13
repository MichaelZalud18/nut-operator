package audit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"
)

// BoundedStore bounds each context-aware database operation. A timeout opens a
// reconcile-local failure latch so each later record does not spend another timeout.
type BoundedStore struct {
	Store
	limit   time.Duration
	mu      sync.Mutex
	failure error
}

func NewBoundedStore(store Store, limit time.Duration) (*BoundedStore, error) {
	if store == nil || limit <= 0 {
		return nil, errors.New("bounded audit store requires a store and positive timeout")
	}
	return &BoundedStore{Store: store, limit: limit}, nil
}

func (s *BoundedStore) begin(ctx context.Context) (context.Context, context.CancelFunc, error) {
	s.mu.Lock()
	failure := s.failure
	s.mu.Unlock()
	if failure != nil {
		return nil, nil, failure
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	child, cancel := context.WithTimeout(ctx, s.limit)
	return child, cancel, nil
}

func (s *BoundedStore) finish(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		err = errors.Join(err, ctx.Err())
		s.mu.Lock()
		if s.failure == nil {
			s.failure = fmt.Errorf("audit I/O deadline or cancellation reached: %w", err)
		}
		s.mu.Unlock()
	}
	return err
}

func (s *BoundedStore) RecordPowerEvent(ctx context.Context, record PowerEvent) error {
	child, cancel, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	return s.finish(child, s.Store.RecordPowerEvent(child, record))
}

func (s *BoundedStore) RecordTelemetrySnapshot(ctx context.Context, record TelemetrySnapshot) error {
	child, cancel, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	return s.finish(child, s.Store.RecordTelemetrySnapshot(child, record))
}

func (s *BoundedStore) RecordCapabilityProfileMatch(ctx context.Context, record CapabilityProfileMatch) error {
	child, cancel, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	return s.finish(child, s.Store.RecordCapabilityProfileMatch(child, record))
}

func (s *BoundedStore) RecordCapabilityProfileVerification(ctx context.Context, record CapabilityProfileVerification) error {
	child, cancel, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	return s.finish(child, s.Store.RecordCapabilityProfileVerification(child, record))
}

func (s *BoundedStore) RecordShutdownFlowCompilation(ctx context.Context, record ShutdownFlowCompilation) error {
	child, cancel, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	return s.finish(child, s.Store.RecordShutdownFlowCompilation(child, record))
}

func (s *BoundedStore) RecordShutdownFlowDecision(ctx context.Context, record ShutdownFlowDecision) error {
	child, cancel, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	return s.finish(child, s.Store.RecordShutdownFlowDecision(child, record))
}

func (s *BoundedStore) RecordShutdownFlowExecution(ctx context.Context, record ShutdownFlowExecution) error {
	child, cancel, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	return s.finish(child, s.Store.RecordShutdownFlowExecution(child, record))
}

func (s *BoundedStore) RecordShutdownFlowExecutionWave(ctx context.Context, record ShutdownFlowExecutionWave) error {
	child, cancel, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	return s.finish(child, s.Store.RecordShutdownFlowExecutionWave(child, record))
}

func (s *BoundedStore) RecordShutdownFlowExecutionGroup(ctx context.Context, record ShutdownFlowExecutionGroup) error {
	child, cancel, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	return s.finish(child, s.Store.RecordShutdownFlowExecutionGroup(child, record))
}

func (s *BoundedStore) RecordShutdownFlowActionAttempt(ctx context.Context, record ShutdownFlowActionAttempt) error {
	child, cancel, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	return s.finish(child, s.Store.RecordShutdownFlowActionAttempt(child, record))
}

func (s *BoundedStore) RecordNodeRelease(ctx context.Context, record NodeReleaseRecord) error {
	child, cancel, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	return s.finish(child, s.Store.RecordNodeRelease(child, record))
}

func (s *BoundedStore) RecordNodeSignalHandoff(ctx context.Context, record NodeSignalHandoff) error {
	child, cancel, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	return s.finish(child, s.Store.RecordNodeSignalHandoff(child, record))
}

func (s *BoundedStore) UpsertExecutorResumeState(ctx context.Context, record ExecutorResumeState) error {
	child, cancel, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	return s.finish(child, s.Store.UpsertExecutorResumeState(child, record))
}

func (s *BoundedStore) EnsureSchema(ctx context.Context) error {
	child, cancel, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	return s.finish(child, s.Store.EnsureSchema(child))
}
func (s *BoundedStore) EnforceRetention(ctx context.Context, now time.Time) error {
	child, cancel, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	return s.finish(child, s.Store.EnforceRetention(child, now))
}
func (s *BoundedStore) GroupDurations(ctx context.Context, flow, hash string, limit int) ([]GroupDurationSample, error) {
	child, cancel, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	values, err := s.Store.GroupDurations(child, flow, hash, limit)
	return values, s.finish(child, err)
}
func (s *BoundedStore) ExecutorResumeState(ctx context.Context, id string) (*ExecutorResumeState, error) {
	child, cancel, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	value, err := s.Store.ExecutorResumeState(child, id)
	return value, s.finish(child, err)
}
func (s *BoundedStore) ExecutionGroupProgress(ctx context.Context, id string) ([]ExecutionGroupProgress, error) {
	child, cancel, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	values, err := s.Store.ExecutionGroupProgress(child, id)
	return values, s.finish(child, err)
}

// UnavailableStore keeps failures explicit while a configured spool captures writes.
func UnavailableStore(cause error) Store {
	if cause == nil {
		cause = errors.New("audit database unavailable")
	}
	return &SQLStore{executor: unavailableSQL{cause}, schema: DefaultSchema, quotedSchema: `"power"`}
}

type unavailableSQL struct{ cause error }

func (s unavailableSQL) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return nil, s.cause
}
func (s unavailableSQL) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, s.cause
}
