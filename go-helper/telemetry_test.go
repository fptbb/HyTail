package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCountersRepresentForwardedPackets(t *testing.T) {
	var s packetStats
	s.recordTX(100)
	s.recordTX(200)
	s.recordRX(80)
	s.recordRX(0)
	got := s.snapshot("join")
	if got.TxPackets != 2 || got.RxPackets != 1 || got.TxBytes != 300 || got.RxBytes != 80 {
		t.Fatalf("wrong tunnel counters: %+v", got)
	}
	if got.LastActivityUnix <= 0 {
		t.Fatal("activity timestamp missing")
	}
	path := filepath.Join(t.TempDir(), "telemetry.json")
	if err := saveStats(path, got); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var persisted statsSnapshot
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.TxPackets != 2 || persisted.RxPackets != 1 {
		t.Fatalf("broken serialized telemetry: %+v", persisted)
	}
}

func TestReliabilityStateIsPersisted(t *testing.T) {
	var s packetStats
	s.setDetails("join", "both", "127.0.0.1", 7777)
	s.setPorts([]int{7777, 7778}, []int{30000, 30001})
	s.setConnection("reconnecting", "temporary outage", 3, 4)
	s.setLocalPolicy("ephemeral")
	got := s.snapshot("")
	if got.Phase != "reconnecting" || got.LastError != "temporary outage" || got.RetryAttempt != 3 || got.RetrySeconds != 4 || got.LocalPortPolicy != "ephemeral" {
		t.Fatalf("lost recovery metadata: %+v", got)
	}
	s.setConnection("online", "", 0, 0)
	got = s.snapshot("")
	if got.LastConnectedUnix == 0 || got.RetryAttempt != 0 || got.LastError != "" {
		t.Fatalf("recovery not recorded: %+v", got)
	}
	s.recordRX(10)
	if s.snapshot("").ServiceStatus != "application-replied" {
		t.Fatal("confirmed reply not reflected in diagnostics")
	}
	s.setConnection("reconnecting", "peer went offline", 1, 1)
	if s.snapshot("").ServiceStatus == "application-replied" {
		t.Fatal("stale traffic must not imply a live application reply")
	}
}
