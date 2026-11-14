// SPDX-License-Identifier: MIT
package portset

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
)

// Bound owns all selected sockets until Close is called. Do not probe then
// release ports: holding every listener eliminates the check/bind race.
type Bound struct {
	TCP  *net.TCPListener
	UDP  *net.UDPConn
	Port int
}

func (b Bound) Close() {
	if b.TCP != nil {
		_ = b.TCP.Close()
	}
	if b.UDP != nil {
		_ = b.UDP.Close()
	}
}
func CloseAll(groups []Bound) {
	for _, b := range groups {
		b.Close()
	}
}

func bindOne(ip net.IP, port int, proto string) (Bound, error) {
	b := Bound{Port: port}
	var err error
	if proto == "tcp" || proto == "both" {
		b.TCP, err = net.ListenTCP("tcp4", &net.TCPAddr{IP: ip, Port: port})
		if err != nil {
			return Bound{}, err
		}
		if port == 0 {
			b.Port = b.TCP.Addr().(*net.TCPAddr).Port
		}
	}
	if proto == "udp" || proto == "both" {
		b.UDP, err = net.ListenUDP("udp4", &net.UDPAddr{IP: ip, Port: b.Port})
		if err != nil {
			b.Close()
			return Bound{}, err
		}
		if port == 0 && b.TCP == nil {
			b.Port = b.UDP.LocalAddr().(*net.UDPAddr).Port
		}
	}
	return b, nil
}

func tryGroup(ip net.IP, ports []int, proto string, ephemeral bool) ([]Bound, error) {
	out := make([]Bound, 0, len(ports))
	for i, port := range ports {
		if ephemeral {
			if i == 0 {
				port = 0
			} else {
				port = out[0].Port + (ports[i] - ports[0])
			}
		}
		if port < 0 || port > 65535 {
			CloseAll(out)
			return nil, syscall.EADDRINUSE
		}
		b, err := bindOne(ip, port, proto)
		if err != nil {
			CloseAll(out)
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}
func busy(err error) bool {
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

// Reserve keeps all TCP/UDP sockets for all ports bound atomically as a group.
// Fallback moves each port by the same offset; the client never silently
// remaps a single remote service port to another unrelated local port.
func Reserve(ip net.IP, preferred []int, proto string) ([]Bound, error) {
	if len(preferred) == 0 || len(preferred) > MaxPorts || (proto != "tcp" && proto != "udp" && proto != "both") {
		return nil, fmt.Errorf("invalid port list or protocol")
	}
	seen := map[int]bool{}
	for _, p := range preferred {
		if p < 1 || p > 65535 || seen[p] {
			return nil, fmt.Errorf("invalid or repeated local port %d", p)
		}
		seen[p] = true
	}
	for delta := 0; delta <= 64; delta++ {
		shifts := []int{delta}
		if delta > 0 {
			shifts = append(shifts, -delta)
		}
		for _, shift := range shifts {
			candidate := make([]int, len(preferred))
			valid := true
			for i, p := range preferred {
				candidate[i] = p + shift
				if candidate[i] < 1 || candidate[i] > 65535 {
					valid = false
					break
				}
			}
			if !valid {
				continue
			}
			bound, err := tryGroup(ip, candidate, proto, false)
			if err == nil {
				return bound, nil
			}
			if !busy(err) {
				return nil, fmt.Errorf("cannot listen on local ports: %w", err)
			}
		}
	}
	for i := 0; i < 24; i++ {
		bound, err := tryGroup(ip, preferred, proto, true)
		if err == nil {
			return bound, nil
		}
		if !busy(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("cannot reserve port group: exhausted neighboring and ephemeral ports")
}

func LocalPorts(bound []Bound) []int {
	out := make([]int, len(bound))
	for i, b := range bound {
		out[i] = b.Port
	}
	return out
}

// Strategy reports how the complete local group was allocated. Every member
// always keeps its relative offset; an arbitrary per-port remap is forbidden.
func Strategy(preferred, actual []int) string {
	if len(preferred) == 0 || len(preferred) != len(actual) {
		return "invalid"
	}
	shift := actual[0] - preferred[0]
	for i := range preferred {
		if actual[i]-preferred[i] != shift {
			return "invalid"
		}
	}
	if shift == 0 {
		return "requested"
	}
	if shift >= -64 && shift <= 64 {
		return "nearby"
	}
	return "ephemeral"
}
