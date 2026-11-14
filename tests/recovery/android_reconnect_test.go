// SPDX-License-Identifier: MIT
// Offline Android bridge regression using a fake unreachable Tailcat client.
// This file is copied into the temporary Go module by test-recovery-stub.sh.
package androidbridge

import (
	"encoding/json"
	"net"
	"strconv"
	"testing"
	"time"
)

func TestAndroidBackgroundReconnectPreservesLocalEndpoint(t *testing.T) {
	_ = Stop()
	v := invite{Version: 1, Address: "tcStub", RemotePort: 25565, Protocol: "tcp", Name: "Minecraft", Preset: "minecraft", ExpiresAt: "2099-01-01T00:00:00Z"}
	code, err := pack(v)
	if err != nil {
		t.Fatal(err)
	}
	response := StartJoin("TCBR1:"+code, "loopback", "")
	var initial struct {
		Status    string `json:"status"`
		LocalPort int    `json:"localPort"`
	}
	if err := json.Unmarshal([]byte(response), &initial); err != nil {
		t.Fatal(err)
	}
	if initial.Status != "running" || initial.LocalPort == 0 {
		t.Fatalf("join did not start: %s", response)
	}
	defer Stop()

	var current struct {
		Phase        string `json:"phase"`
		LocalPort    int    `json:"localPort"`
		RetryAttempt int    `json:"retryAttempt"`
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_ = json.Unmarshal([]byte(Status()), &current)
		if current.Phase == "reconnecting" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if current.Phase != "reconnecting" || current.LocalPort != initial.LocalPort || current.RetryAttempt < 1 {
		t.Fatalf("incorrect reconnect state: %s", Status())
	}
	_ = NetworkChanged()
	time.Sleep(100 * time.Millisecond)
	_ = json.Unmarshal([]byte(Status()), &current)
	if current.LocalPort != initial.LocalPort {
		t.Fatal("network change changed local port")
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(initial.LocalPort))
	if probe, e := net.Listen("tcp", addr); e == nil {
		probe.Close()
		t.Fatal("local listener was released during retry")
	}

	Stop()
	reusable, e := net.Listen("tcp", addr)
	if e != nil {
		t.Fatalf("port not released after Stop: %v", e)
	}
	reusable.Close()
}
