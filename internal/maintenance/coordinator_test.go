package maintenance

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCoordinatorSingleFlight(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int64
	coordinator := New(func(context.Context) (Result, error) {
		calls.Add(1)
		close(started)
		<-release
		return Result{RemovedObjects: 2, RemovedBytes: 10}, nil
	})

	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		coordinator.Run(context.Background(), TriggerSchedule)
	}()
	<-started
	current, accepted := coordinator.Run(context.Background(), TriggerOperator)
	if accepted || current.Trigger != TriggerSchedule {
		t.Fatalf("concurrent run accepted=%v run=%#v", accepted, current)
	}
	if snapshot := coordinator.Snapshot(); snapshot.State != "running" || snapshot.Current == nil {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	close(release)
	wait.Wait()
	if calls.Load() != 1 {
		t.Fatalf("runner calls = %d", calls.Load())
	}
	snapshot := coordinator.Snapshot()
	if snapshot.State != "idle" || snapshot.Last == nil || snapshot.Last.Result.RemovedObjects != 2 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestCoordinatorRecordsFailure(t *testing.T) {
	coordinator := New(func(context.Context) (Result, error) {
		return Result{SkippedObjects: 1}, errors.New("disk error")
	})
	run, accepted := coordinator.Run(context.Background(), TriggerPressure)
	if !accepted || run.Error != "disk error" || run.Result.Errors != 1 || run.FinishedAt == nil {
		t.Fatalf("run = %#v accepted=%v", run, accepted)
	}
}

func TestCoordinatorTimestampsUseUTC(t *testing.T) {
	coordinator := New(func(context.Context) (Result, error) { return Result{}, nil })
	coordinator.now = func() time.Time {
		return time.Date(2026, 8, 10, 1, 0, 0, 0, time.FixedZone("CEST", 2*60*60))
	}
	run, _ := coordinator.Run(context.Background(), TriggerStartup)
	if run.StartedAt.Location() != time.UTC || run.FinishedAt == nil || run.FinishedAt.Location() != time.UTC {
		t.Fatalf("timestamps = %#v", run)
	}
}
