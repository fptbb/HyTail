// SPDX-License-Identifier: MIT
package main

import (
	"net"
	"testing"
	"time"
)

func TestOnlyAssignedPrivateIPv4IsValid(t *testing.T) {
	for _, text := range []string{"10.5.4.3", "172.16.0.1", "172.31.2.1", "192.168.1.22"} {
		if !validPrivateIPv4(net.ParseIP(text)) {
			t.Errorf("expected private: %s", text)
		}
	}
	for _, text := range []string{"127.0.0.1", "8.8.8.8", "100.64.0.1", "169.254.0.1", "0.0.0.0", "not-an-ip", "::1"} {
		if validPrivateIPv4(net.ParseIP(text)) {
			t.Errorf("expected rejection: %s", text)
		}
	}
}

func TestInterfacePriority(t *testing.T) {
	if nicPriority("wlan0") <= nicPriority("docker0") {
		t.Fatal("physical NIC should outrank Docker")
	}
	if nicPriority("enp4s0") <= nicPriority("vethabc") {
		t.Fatal("physical NIC should outrank veth")
	}
}

// The kernel must deliver a local UDP packet addressed to the selected LAN
// IP with a matching private source address. This does not contact a remote
// host or DERP relay.
func TestLocalTrafficUsesPrivateSource(t *testing.T) {
	ip, err := chosenLocalIP()
	if err != nil {
		t.Skipf("no active RFC1918 interface: %v", err)
	}
	listener, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ip, Port: 0})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	sender, err := net.DialUDP("udp4", nil, listener.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sender.Close()
	if err := listener.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := sender.Write([]byte("local-test")); err != nil {
		t.Fatalf("send: %v", err)
	}
	buf := make([]byte, 256)
	n, from, err := listener.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if string(buf[:n]) != "local-test" || !from.IP.Equal(ip) {
		t.Errorf("unexpected source %v or payload %q, chosen IP %v", from, buf[:n], ip)
	}
}
