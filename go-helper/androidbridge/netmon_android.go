// SPDX-License-Identifier: MIT
//go:build android

package androidbridge

import (
	"errors"
	"net"
	"time"

	"tailscale.com/net/netmon"
)

// Android apps cannot enumerate interfaces through Go's net.Interfaces:
// Android blocks the NETLINK_ROUTE queries used by that API for unprivileged
// applications. Tailcat uses Tailscale netmon during startup, so provide
// an Android-specific interface snapshot through Tailscale's documented hook.
// This must be registered before tailcat.Server.Start or Client.Ping.
func init() {
	netmon.RegisterInterfaceGetter(androidInterfaces)
}

// androidInterfaces deliberately avoids net.Interfaces and Interface.Addrs.
// An unconnected UDP Dial selects a local source IP without sending traffic.
// The same technique is used by Tailscale's Android binary fallback.
// The snapshot is regenerated for every netmon poll, so changing Wi-Fi or
// cellular network will be reflected in subsequent polls.
func androidInterfaces() ([]netmon.Interface, error) {
	var addresses []net.Addr
	if ip := androidOutboundIP("udp4", "8.8.8.8:53"); ip != nil {
		addresses = append(addresses, &net.IPNet{IP: ip, Mask: net.CIDRMask(32, 32)})
	}
	if ip := androidOutboundIP("udp6", "[2001:4860:4860::8888]:53"); ip != nil {
		addresses = append(addresses, &net.IPNet{IP: ip, Mask: net.CIDRMask(128, 128)})
	}
	if len(addresses) == 0 {
		return nil, errors.New("Android has no usable outbound network route (check Wi-Fi or mobile data)")
	}
	return []netmon.Interface{{
		Interface: &net.Interface{
			Name:  "android",
			MTU:   1500,
			Flags: net.FlagUp | net.FlagRunning,
		},
		AltAddrs: addresses,
	}}, nil
}

func androidOutboundIP(network, destination string) net.IP {
	// Dialing UDP does not contact this destination. The kernel merely
	// selects the route and local source address, which Android permits.
	conn, err := (&net.Dialer{Timeout: 2 * time.Second}).Dial(network, destination)
	if err != nil {
		return nil
	}
	defer conn.Close()
	local, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || local.IP == nil || local.IP.IsUnspecified() || local.IP.IsLoopback() {
		return nil
	}
	return local.IP
}
