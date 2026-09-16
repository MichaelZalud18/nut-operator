package controller

import (
	"context"
	"testing"
	"time"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestFlowRunsOwnership(t *testing.T) {
	runs := newFlowRuns(2)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { _ = runs.Start(ctx); close(stopped) }()
	<-runs.ready
	t.Cleanup(func() { cancel(); <-stopped })
	flow := &power.ShutdownFlow{ObjectMeta: metav1.ObjectMeta{Name: "first", UID: "first", Generation: 1}}
	started := make(chan struct{})
	finished := make(chan struct{})
	work := func(ctx context.Context, flow *power.ShutdownFlow, publish func()) {
		close(started)
		flow.Status.Phase = power.ShutdownFlowPhaseRunning
		publish()
		<-ctx.Done()
		close(finished)
	}
	if !runs.submit(flow, time.Second, work) {
		t.Fatal("first run rejected")
	}
	<-started
	if runs.submit(flow, time.Second, work) {
		t.Fatal("duplicate run admitted")
	}
	second := flow.DeepCopy()
	second.Name, second.UID = "second", "second"
	if !runs.submit(second, time.Second, func(context.Context, *power.ShutdownFlow, func()) {}) {
		t.Fatal("independent run rejected")
	}
	third := flow.DeepCopy()
	third.Name, third.UID = "third", "third"
	if runs.submit(third, time.Second, work) {
		t.Fatal("capacity bound exceeded")
	}
	runs.cancel(flow.Name)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("cancellation not delivered")
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("manager did not join workers")
	}
	if runs.submit(third, time.Second, work) {
		t.Fatal("work admitted after shutdown")
	}
}

func TestFlowRunsClaims(t *testing.T) {
	runs := newFlowRuns(2)
	if err := runs.claim("a", []string{"node:a"}); err != nil {
		t.Fatal(err)
	}
	if err := runs.claim("b", []string{"node:b"}); err != nil {
		t.Fatal(err)
	}
	if err := runs.claim("b", []string{"node:a"}); err == nil {
		t.Fatal("overlap admitted")
	}
	if err := runs.claim("c", []string{"*"}); err == nil {
		t.Fatal("unbounded effects admitted")
	}
	runs.release("a")
	if err := runs.claim("b", []string{"node:a"}); err != nil {
		t.Fatal(err)
	}
	runs.release("b")
	if err := runs.claim("c", []string{"*"}); err != nil {
		t.Fatal(err)
	}
	if err := runs.claim("a", []string{"node:a"}); err == nil {
		t.Fatal("global owner ignored")
	}
}
