package storage

import (
	"math"
	"sync"
	"testing"
)

func TestControllerReservationsPreventConcurrentOvercommit(t *testing.T) {
	controller := NewController(100, 0.9, 0.75, 0, 0)
	var wait sync.WaitGroup
	start := make(chan struct{})
	results := make(chan *Reservation, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			results <- controller.Reserve(60, 1_000)
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	accepted := 0
	for reservation := range results {
		if reservation != nil {
			accepted++
			reservation.Release()
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted reservations = %d, want 1", accepted)
	}
	if snapshot := controller.Snapshot(); snapshot.ReservedBytes != 0 || snapshot.BypassObjects != 1 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestControllerEnforcesFilesystemReserveAndTracksBypass(t *testing.T) {
	controller := NewController(1_000, 0.9, 0.75, 100, 300)
	if reservation := controller.Reserve(150, 200); reservation != nil {
		t.Fatal("reservation accepted below filesystem reserve")
	}
	snapshot := controller.Snapshot()
	if snapshot.BypassObjects != 1 || snapshot.BypassBytes != 150 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestControllerReservationsShareFilesystemReserve(t *testing.T) {
	controller := NewController(0, 0.9, 0.75, 50, 0)
	first := controller.Reserve(60, 150)
	if first == nil {
		t.Fatal("first reservation rejected")
	}
	if second := controller.Reserve(60, 150); second != nil {
		t.Fatal("second reservation overcommitted filesystem reserve")
	}
	first.Release()
}

func TestControllerCommitAndPressure(t *testing.T) {
	controller := NewController(100, 0.9, 0.75, 0, 80)
	reservation := controller.Reserve(10, 1_000)
	if reservation == nil {
		t.Fatal("reservation rejected")
	}
	if !controller.Snapshot().Pressure {
		t.Fatal("expected pressure while reservation reaches high watermark")
	}
	if !reservation.Commit(9) {
		t.Fatal("valid reservation did not commit")
	}
	reservation.Commit(9)
	snapshot := controller.Snapshot()
	if snapshot.CommittedBytes != 89 || snapshot.ReservedBytes != 0 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestControllerRejectsCommitLargerThanReservation(t *testing.T) {
	controller := NewController(100, 0.9, 0.75, 0, 0)
	reservation := controller.Reserve(10, 1_000)
	if reservation == nil {
		t.Fatal("reservation rejected")
	}
	if reservation.Commit(11) {
		t.Fatal("oversized commit accepted")
	}
	if snapshot := controller.Snapshot(); snapshot.CommittedBytes != 0 || snapshot.ReservedBytes != 0 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestControllerDoesNotOverflowAtIntegerBoundary(t *testing.T) {
	controller := NewController(math.MaxInt64, 0.9, 0.75, 0, math.MaxInt64-5)
	if reservation := controller.Reserve(10, math.MaxInt64); reservation != nil {
		t.Fatal("overflowing reservation accepted")
	}
	if snapshot := controller.Snapshot(); snapshot.BypassBytes != 10 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestControllerBypassesUnknownSizes(t *testing.T) {
	controller := NewController(100, 0.9, 0.75, 0, 0)
	if reservation := controller.Reserve(-1, 1_000); reservation != nil {
		t.Fatal("unknown size must bypass cache")
	}
}
