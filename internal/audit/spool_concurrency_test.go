package audit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type appendDuringReplay struct {
	NoopStore
	append func() error
}

func (w appendDuringReplay) RecordPowerEvent(context.Context, PowerEvent) error { return w.append() }

func TestReplayPreservesConcurrentAppend(t *testing.T) {
	dir := t.TempDir()
	spool, err := NewSpoolWriter(failingAuditWriter{err: errors.New("offline")}, SpoolOptions{Directory: dir, DisableSync: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := spool.RecordPowerEvent(ctx, PowerEvent{EventID: "first"}); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	writer := appendDuringReplay{append: func() error {
		var appendErr error
		once.Do(func() { appendErr = spool.RecordPowerEvent(ctx, PowerEvent{EventID: "during-replay"}) })
		return appendErr
	}}
	if _, err := ReplaySpool(ctx, writer, ReplayOptions{Directory: dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, defaultSpoolFileName)); err != nil {
		t.Fatal("replay removed concurrently appended evidence", err)
	}
	replayed := &recordingWriter{}
	if _, err := ReplaySpool(ctx, replayed, ReplayOptions{Directory: dir}); err != nil {
		t.Fatal(err)
	}
	if len(replayed.events) != 2 || replayed.events[1].EventID != "during-replay" {
		t.Fatalf("lost evidence: %+v", replayed.events)
	}
}

func TestConcurrentSpoolWritersRespectSharedCap(t *testing.T) {
	dir := t.TempDir()
	const capBytes = 1024
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			spool, err := NewSpoolWriter(failingAuditWriter{err: errors.New("offline")}, SpoolOptions{Directory: dir, DisableSync: true, MaxBytes: capBytes})
			if err != nil {
				t.Error(err)
				return
			}
			if err := spool.RecordPowerEvent(context.Background(), PowerEvent{EventID: "event"}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	info, err := os.Stat(filepath.Join(dir, defaultSpoolFileName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > capBytes {
		t.Fatalf("shared spool cap exceeded: %d > %d", info.Size(), capBytes)
	}
}
