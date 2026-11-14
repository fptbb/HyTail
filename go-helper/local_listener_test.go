// SPDX-License-Identifier: MIT
package main

import (
	"errors"
	"fmt"
	"net"
	"syscall"
	"testing"
)

var testLoopbackIP = net.IPv4(127, 0, 0, 1)

func TestAddressInUseDetectionAcrossPlatforms(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "unix errno", err: syscall.EADDRINUSE, want: true},
		{name: "windows winsock errno", err: fmt.Errorf("listen: %w", syscall.Errno(10048)), want: true},
		{name: "windows message", err: errors.New("bind: Only one usage of each socket address (protocol/network address/port) is normally permitted."), want: true},
		{name: "unix message", err: errors.New("bind: address already in use"), want: true},
		{name: "unrelated error", err: errors.New("permission denied"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAddressInUse(tt.err); got != tt.want {
				t.Fatalf("isAddressInUse(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestPreferredLocalTCPPort(t *testing.T) {
	first, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: testLoopbackIP, Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(first.Addr().(*net.TCPAddr).Port)
	first.Close()

	tcp, udp, actual, err := listenLocalBridge(testLoopbackIP, port, protoTCP)
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	if udp != nil || actual != int(port) {
		t.Fatalf("expected TCP port %d, got %d", port, actual)
	}
}

func TestOccupiedLocalTCPPortFallsBackNearby(t *testing.T) {
	occupied, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: testLoopbackIP, Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	preferred := uint16(occupied.Addr().(*net.TCPAddr).Port)

	tcp, udp, actual, err := listenLocalBridge(testLoopbackIP, preferred, protoTCP)
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	if udp != nil || actual == int(preferred) || absPortDistance(actual, int(preferred)) > 64 {
		t.Fatalf("wanted nearby alternative to %d, got %d", preferred, actual)
	}
}

func TestOccupiedLocalUDPPortFallsBackNearby(t *testing.T) {
	occupied, err := net.ListenUDP("udp4", &net.UDPAddr{IP: testLoopbackIP, Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	preferred := uint16(occupied.LocalAddr().(*net.UDPAddr).Port)

	tcp, udp, actual, err := listenLocalBridge(testLoopbackIP, preferred, protoUDP)
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	if tcp != nil || actual == int(preferred) || absPortDistance(actual, int(preferred)) > 64 {
		t.Fatalf("wanted nearby UDP alternative to %d, got %d", preferred, actual)
	}
}

func TestBothProtocolsShareFreeLocalPortAndReleaseFailedTCPBind(t *testing.T) {
	occupied, err := net.ListenUDP("udp4", &net.UDPAddr{IP: testLoopbackIP, Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	preferred := uint16(occupied.LocalAddr().(*net.UDPAddr).Port)

	tcp, udp, actual, err := listenLocalBridge(testLoopbackIP, preferred, protoBoth)
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	defer udp.Close()
	if actual == int(preferred) || actual != tcp.Addr().(*net.TCPAddr).Port || actual != udp.LocalAddr().(*net.UDPAddr).Port {
		t.Fatalf("TCP and UDP must share a fallback port, got %d", actual)
	}
	// The failed two-protocol reservation must not leak a TCP socket on
	// the original port (the UDP listener is still occupied there).
	probe, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: testLoopbackIP, Port: int(preferred)})
	if err != nil {
		t.Fatalf("leaked TCP listener on rejected port: %v", err)
	}
	probe.Close()
}

func TestInvalidLocalPortAndProtocol(t *testing.T) {
	for _, p := range []protocol{protoTCP, protoUDP, protoBoth} {
		if _, _, _, err := listenLocalBridge(testLoopbackIP, 0, p); err == nil {
			t.Fatal("accepted port zero")
		}
	}
	if _, _, _, err := listenLocalBridge(testLoopbackIP, 20000, protocol("invalid")); err == nil {
		t.Fatal("accepted invalid protocol")
	}
}

func absPortDistance(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}
