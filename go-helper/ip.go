// SPDX-License-Identifier: MIT
// Private interface selection for a same-device HyTail UDP bridge.
package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
)

// chosenLocalIP returns an RFC1918 address assigned to an active non-loopback
// interface. DialUDP to a TEST-NET address only asks the kernel which route it
// would use; it sends no packet. Prefer that interface to Docker/VM adapters.
// HYTAIL_LOCAL_IP allows selecting another *assigned* private IPv4 when
// the default route is through a VPN or a container network.
func chosenLocalIP() (net.IP, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("enumerate network interfaces: %w", err)
	}
	type candidate struct {
		ip   net.IP
		name string
	}
	var choices []candidate
	for _, nic := range interfaces {
		if nic.Flags&net.FlagUp == 0 || nic.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := nic.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if validPrivateIPv4(ip) {
				choices = append(choices, candidate{ip: ip.To4(), name: nic.Name})
			}
		}
	}
	if len(choices) == 0 {
		return nil, errors.New("no active RFC1918 IPv4 interface; connect to a private LAN or supply an assigned private address")
	}

	configured := strings.TrimSpace(os.Getenv("HYTAIL_LOCAL_IP"))
	if configured == "" {
		configured = strings.TrimSpace(os.Getenv("HYTAILSCALE_LOCAL_IP"))
	}
	if configured != "" {
		ip := net.ParseIP(configured)
		if !validPrivateIPv4(ip) {
			return nil, fmt.Errorf("HYTAIL_LOCAL_IP %q is not a private IPv4 address", configured)
		}
		for _, entry := range choices {
			if entry.ip.Equal(ip) {
				return entry.ip, nil
			}
		}
		return nil, fmt.Errorf("HYTAIL_LOCAL_IP %q is not assigned to an active interface", configured)
	}

	// Use the source address on the default IPv4 route where possible.
	// DialUDP on an unconnected socket does not send a packet.
	probe, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 9})
	if err == nil {
		ip := probe.LocalAddr().(*net.UDPAddr).IP.To4()
		_ = probe.Close()
		for _, entry := range choices {
			if entry.ip.Equal(ip) {
				return entry.ip, nil
			}
		}
	}
	// Offline / no default route: prefer likely physical NICs over virtual ones.
	sort.SliceStable(choices, func(i, j int) bool {
		return nicPriority(choices[i].name) > nicPriority(choices[j].name)
	})
	return choices[0].ip, nil
}

func validPrivateIPv4(ip net.IP) bool {
	v4 := ip.To4()
	return v4 != nil && v4.IsPrivate() && !v4.IsLoopback() && !v4.IsUnspecified()
}

func nicPriority(name string) int {
	n := strings.ToLower(name)
	for _, prefix := range []string{"docker", "podman", "veth", "br-", "virbr", "tailscale", "wg", "tun", "tap"} {
		if strings.HasPrefix(n, prefix) {
			return 0
		}
	}
	for _, prefix := range []string{"en", "eth", "wl", "wlan", "wifi", "wi-fi", "ethernet"} {
		if strings.HasPrefix(n, prefix) {
			return 2
		}
	}
	return 1
}
