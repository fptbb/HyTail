// SPDX-License-Identifier: MIT
package main

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
)

// tryLocalListeners reserves the selected local TCP and/or UDP sockets.
// For both protocols, the sockets must share the same assigned port.
// Partially-created listeners are always closed before returning an error.
func tryLocalListeners(ip net.IP, port int, proto protocol) (*net.TCPListener, *net.UDPConn, int, error) {
	var tcp *net.TCPListener
	var udp *net.UDPConn
	var err error
	selected := port

	if proto.hasTCP() {
		tcp, err = net.ListenTCP("tcp4", &net.TCPAddr{IP: ip, Port: selected})
		if err != nil {
			return nil, nil, 0, err
		}
		selected = tcp.Addr().(*net.TCPAddr).Port
	}
	if proto.hasUDP() {
		udp, err = net.ListenUDP("udp4", &net.UDPAddr{IP: ip, Port: selected})
		if err != nil {
			if tcp != nil {
				_ = tcp.Close()
			}
			return nil, nil, 0, err
		}
		selected = udp.LocalAddr().(*net.UDPAddr).Port
	}
	return tcp, udp, selected, nil
}

// isAddressInUse recognizes address-in-use errors across operating systems.
// Windows reports Winsock WSAEADDRINUSE (10048), whose text differs from the
// Unix EADDRINUSE message and whose errno is not syscall.EADDRINUSE.
func isAddressInUse(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.EADDRINUSE) || errors.Is(err, syscall.Errno(10048)) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "address already in use") ||
		strings.Contains(message, "only one usage of each socket address")
}

// listenLocalBridge tries the remote service's port locally first, then
// neighboring ports, so the joiner can use Hytale/Minecraft's usual port if
// available. The remote Tailcat port NEVER changes. If nearby ports are all
// occupied, let the OS pick a free ephemeral port as a last resort.
// The returned sockets remain bound until the bridge stops; there is no
// separate probe-and-bind race.
func listenLocalBridge(ip net.IP, preferred uint16, proto protocol) (*net.TCPListener, *net.UDPConn, int, error) {
	const radius = 64
	if preferred == 0 || !proto.valid() {
		return nil, nil, 0, fmt.Errorf("invalid local port or protocol")
	}
	original := int(preferred)
	for offset := 0; offset <= radius; offset++ {
		candidates := []int{original + offset}
		if offset != 0 {
			candidates = append(candidates, original-offset)
		}
		for _, candidate := range candidates {
			if candidate < 1 || candidate > 65535 {
				continue
			}
			tcp, udp, assigned, err := tryLocalListeners(ip, candidate, proto)
			if err == nil {
				return tcp, udp, assigned, nil
			}
			if !isAddressInUse(err) {
				return nil, nil, 0, fmt.Errorf("cannot listen on %s:%d: %w", ip, candidate, err)
			}
		}
	}
	// Port 0 delegates allocation to the OS. For TCP+UDP the second bind
	// can race; retry with fresh TCP ports and close any partial allocation.
	for attempt := 0; attempt < 16; attempt++ {
		tcp, udp, assigned, err := tryLocalListeners(ip, 0, proto)
		if err == nil {
			return tcp, udp, assigned, nil
		}
		if !isAddressInUse(err) {
			return nil, nil, 0, fmt.Errorf("cannot bind fallback local port: %w", err)
		}
	}
	return nil, nil, 0, fmt.Errorf("could not reserve a local %s port after 16 fallback attempts", proto)
}
