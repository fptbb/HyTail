// SPDX-License-Identifier: MIT
// Offline integration regression: the remote peer is deliberately unreachable.
// This file is copied into the temporary Go module by test-recovery-stub.sh.
package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tailscale/tailcat"
)

func TestDisconnectedJoinPreservesSocketUntilStop(t *testing.T) {
	ip, err := chosenLocalIP()
	if err != nil {
		t.Skipf("no private IPv4 interface available: %v", err)
	}

	// Hold the preferred port first to exercise deterministic nearby fallback.
	occupied, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: ip, Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	preferred := occupied.Addr().(*net.TCPAddr).Port
	defer occupied.Close()

	dir := t.TempDir()
	ready := filepath.Join(dir, "ready.json")
	stats := filepath.Join(dir, "stats.json")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- join(ctx, tailcat.Addr("tcStub"), []int{25565}, []int{preferred}, protoTCP, ready, stats)
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(4 * time.Second):
			t.Error("join worker leaked")
		}
	}()

	var details struct {
		Port        int  `json:"port"`
		PortChanged bool `json:"portChanged"`
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if raw, e := os.ReadFile(ready); e == nil && json.Unmarshal(raw, &details) == nil && details.Port != 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if details.Port == 0 || !details.PortChanged || details.Port == preferred {
		t.Fatalf("local fallback not advertised: %+v", details)
	}
	if probe, e := net.ListenTCP("tcp4", &net.TCPAddr{IP: ip, Port: details.Port}); e == nil {
		probe.Close()
		t.Fatal("allocated port was not held")
	}

	// Must reach reconnecting after the stub rejects its handshake.
	var snapshot statsSnapshot
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if raw, e := os.ReadFile(stats); e == nil {
			_ = json.Unmarshal(raw, &snapshot)
		}
		if snapshot.Phase == "reconnecting" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if snapshot.Phase != "reconnecting" {
		t.Fatalf("expected reconnecting, got %q", snapshot.Phase)
	}
	if len(snapshot.LocalPorts) != 1 || snapshot.LocalPorts[0] != details.Port {
		t.Fatalf("local mapping changed during retry: %+v", snapshot.LocalPorts)
	}
	if snapshot.RetryAttempt < 1 {
		t.Fatal("retry counter did not increment")
	}
	if probe, e := net.ListenTCP("tcp4", &net.TCPAddr{IP: ip, Port: details.Port}); e == nil {
		probe.Close()
		t.Fatal("listener was released during reconnect")
	}

	cancel()
	select {
	case e := <-done:
		if e != nil {
			t.Fatalf("join shutdown: %v", e)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("join did not stop")
	}
	// The deferred cleanup must not consume the already-read done channel.
	done <- nil
	reusable, e := net.ListenTCP("tcp4", &net.TCPAddr{IP: ip, Port: details.Port})
	if e != nil {
		t.Fatalf("port not released on Stop: %v", e)
	}
	reusable.Close()
}
