package main

import (
	"net"
	"testing"
	"time"
)

func TestRejectArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"exit-node"}, {"host", "--port", "0"}, {"host", "--port", "65536"}, {"host", "--port", "80"}, {"host", "--port", "80", "--protocol", "all", "--ready-file", "x", "--stats-file", "y"}, {"host", "--port", "80", "--ready-file", "x", "--stats-file", "y", "--addr-file", "secret"}, {"join", "--port", "80", "--ready-file", "x", "--stats-file", "y"}} {
		if err := run(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
func TestOnlySelectedPortExposed(t *testing.T) {
	ip := net.IPv4(127, 0, 0, 1)
	s := &packetStats{}
	for _, other := range []uint16{0, 1, 22, 53, 80, 443, 25566, 65535} {
		if onServiceUDP(map[uint16]bool{25565: true, 25567: true}, ip, s)(other) != nil || onServiceTCP(map[uint16]bool{25565: true, 25567: true}, ip, s)(other) != nil {
			t.Fatalf("allowed port %d", other)
		}
	}
	if onServiceUDP(map[uint16]bool{25565: true, 25567: true}, ip, s)(25565) == nil || onServiceTCP(map[uint16]bool{25565: true, 25567: true}, ip, s)(25565) == nil {
		t.Fatal("port denied")
	}
}
func TestLocalTargets(t *testing.T) {
	if _, err := resolveTarget("127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	for _, x := range []string{"8.8.8.8", "192.0.2.1", "0.0.0.0", "evil.example"} {
		if _, err := resolveTarget(x); err == nil {
			t.Errorf("target %s unexpectedly accepted", x)
		}
	}
}

func TestTCPHostServiceChecksArePassive(t *testing.T) {
	ip := net.IPv4(127, 0, 0, 1)
	ln, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: ip, Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	var stats packetStats
	stats.setDetails("host", "both", ip.String(), uint16(port))
	stats.initPassiveServices([]int{port}, protoBoth)
	if got := stats.snapshot(""); got.ServiceStatus != "unverified" || len(got.ServicePorts) != 2 {
		t.Fatalf("expected passive diagnostics, got %+v", got)
	}
	// The diagnostics must NEVER create an unsolicited TCP connection.
	if err := ln.SetDeadline(time.Now().Add(150 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if conn, err := ln.AcceptTCP(); err == nil {
		conn.Close()
		t.Fatal("service diagnostic made an unsolicited TCP connection")
	} else if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
		t.Fatalf("unexpected accept failure: %v", err)
	}
	stats.recordServiceTCP(port, true)
	if got := stats.snapshot(""); got.ServiceStatus != "tcp-accepted-udp-unverified" {
		t.Fatalf("no passive success recorded: %+v", got)
	}
	stats.recordServiceTCP(port, false)
	if got := stats.snapshot(""); got.ServiceStatus != "tcp-unreachable" {
		t.Fatalf("no forwarded failure recorded: %+v", got)
	}
}
