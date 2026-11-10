package main

import (
	"context"
	"testing"
	"time"
)

func TestHelperShutdownWaitsForOperationAndExcludesNextWriter(t *testing.T) {
	s := &switcher{}
	s.busy.Store(true)
	done := make(chan bool, 1)
	go func() { done <- reserveHelperShutdown(context.Background(), s) }()
	select {
	case <-done:
		t.Fatal("quit interrupted active operation")
	case <-time.After(70 * time.Millisecond):
	}
	s.opMu.Lock()
	s.busy.Store(false) // active mutation completes while usage check still saves
	select {
	case <-done:
		t.Fatal("quit interrupted usage save")
	case <-time.After(70 * time.Millisecond):
	}
	s.opMu.Unlock()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("shutdown failed")
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not finish")
	}
	if !s.busy.Load() || s.busy.CompareAndSwap(false, true) {
		t.Fatal("next writer could enter before process exit")
	}
}

func TestHelperShutdownCancellationReleasesOnlyItsOwnReservation(t *testing.T) {
	s := &switcher{}
	s.opMu.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Millisecond)
	defer cancel()
	if reserveHelperShutdown(ctx, s) {
		t.Fatal("shutdown ignored unfinished save")
	}
	s.opMu.Unlock()
	if s.busy.Load() {
		t.Fatal("cancelled shutdown left a reservation")
	}
	s.busy.Store(true)
	if reserveHelperShutdown(ctx, s) || !s.busy.Load() {
		t.Fatal("cancelled shutdown cleared another operation")
	}
}
