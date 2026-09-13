package audit

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type stalledAuditStore struct {
	NoopStore
	calls atomic.Int32
}

func (s *stalledAuditStore) RecordPowerEvent(ctx context.Context, _ PowerEvent) error {
	s.calls.Add(1)
	<-ctx.Done()
	return ctx.Err()
}

func TestBoundedStoreTimeoutLatchesAndSpools(t *testing.T) {
	primary := &stalledAuditStore{}
	bounded, err := NewBoundedStore(primary, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	spool, err := NewSpoolWriter(bounded, SpoolOptions{Directory: t.TempDir(), DisableSync: true})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := spool.RecordPowerEvent(context.Background(), PowerEvent{EventID: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	if primary.calls.Load() != 1 || spool.Stats().FallbackWrites != 3 {
		t.Fatalf("calls=%d stats=%+v", primary.calls.Load(), spool.Stats())
	}
	if _, err := bounded.GroupDurations(context.Background(), "flow", "hash", 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("read bypassed timeout latch: %v", err)
	}
}

func TestBoundedStorePreservesParentCancellation(t *testing.T) {
	primary := &stalledAuditStore{}
	bounded, err := NewBoundedStore(primary, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := bounded.RecordPowerEvent(ctx, PowerEvent{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if primary.calls.Load() != 0 {
		t.Fatal("called primary with canceled context")
	}
}

func TestUnavailableStoreDoesNotReportEmptySuccessfulReads(t *testing.T) {
	cause := errors.New("offline")
	store := UnavailableStore(cause)
	if _, err := store.GroupDurations(context.Background(), "flow", "hash", 1); !errors.Is(err, cause) {
		t.Fatal(err)
	}
	if _, err := store.ExecutorResumeState(context.Background(), "id"); !errors.Is(err, cause) {
		t.Fatal(err)
	}
	if _, err := store.ExecutionGroupProgress(context.Background(), "id"); !errors.Is(err, cause) {
		t.Fatal(err)
	}
}
