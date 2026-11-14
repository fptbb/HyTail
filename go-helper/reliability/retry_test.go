package reliability

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBackoffBounded(t *testing.T) {
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second}
	for i, exp := range want {
		if got := Backoff(i + 1); got != exp {
			t.Fatalf("retry %d = %s, want %s", i+1, got, exp)
		}
	}
	if Backoff(200) != MaximumBackoff {
		t.Fatal("backoff uncapped")
	}
}
func TestWaitCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if Wait(ctx, 30*time.Second) || time.Since(start) > time.Second {
		t.Fatal("cancelled wait must return immediately")
	}
}
func TestFailureThreshold(t *testing.T) {
	var f CountFailures
	bad := errors.New("temporary")
	if f.Observe(bad) || f.Observe(bad) {
		t.Fatal("single failures should not disconnect")
	}
	if !f.Observe(bad) {
		t.Fatal("third consecutive failure must reconnect")
	}
	if f.Observe(nil) || f.Observe(bad) {
		t.Fatal("success should reset failures")
	}
}

func TestRecentIncomingTraffic(t *testing.T) {
	now := time.Unix(1700000000, 0)
	if !RecentIncomingTraffic(now.Add(-5*time.Second).Unix(), now) {
		t.Fatal("recent incoming game response not recognized")
	}
	if RecentIncomingTraffic(0, now) || RecentIncomingTraffic(now.Add(-16*time.Second).Unix(), now) || RecentIncomingTraffic(now.Add(time.Hour).Unix(), now) {
		t.Fatal("stale or future activity falsely treated as recent")
	}
}
