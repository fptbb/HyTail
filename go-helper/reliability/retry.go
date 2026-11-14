// SPDX-License-Identifier: MIT
// Package reliability provides cancellation-aware, bounded retry intervals.
package reliability

import (
	"context"
	"time"
)

const (
	ProbeInterval                = 8 * time.Second
	ProbeFailuresBeforeReconnect = 3
	MaximumBackoff               = 30 * time.Second
)

// Backoff is exponential and capped. Attempt numbers are 1-based.
// Using a bounded delay avoids busy loops when the network is unavailable.
func Backoff(attempt int) time.Duration {
	if attempt <= 1 {
		return time.Second
	}
	delay := time.Second
	for i := 1; i < attempt; i++ {
		if delay >= MaximumBackoff/2 {
			return MaximumBackoff
		}
		delay *= 2
	}
	if delay > MaximumBackoff {
		return MaximumBackoff
	}
	return delay
}

func Wait(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// CountFailures tracks consecutive failed health checks; a success resets it.
// Liveness probes must never treat a single temporary timeout as a disconnect.
type CountFailures int

func (f *CountFailures) Observe(err error) bool {
	if err == nil {
		*f = 0
		return false
	}
	*f++
	return int(*f) >= ProbeFailuresBeforeReconnect
}

// RecentIncomingTraffic is stronger evidence of a working tunnel than a
// failed path-discovery probe. A peer response in the last 15 seconds
// suppresses unnecessary reconnection on discovery-only timeouts.
func RecentIncomingTraffic(lastUnix int64, now time.Time) bool {
	if lastUnix <= 0 {
		return false
	}
	age := now.Sub(time.Unix(lastUnix, 0))
	return age >= 0 && age <= 15*time.Second
}
