// SPDX-License-Identifier: MIT
// HyTail's fixed-endpoint, explicitly scoped TCP/UDP bridge.
// Deliberately NO Tailcat exit-node handlers or arbitrary routing.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"example.com/hytail-helper/invitedns"
	"example.com/hytail-helper/portset"
	"example.com/hytail-helper/reliability"
	"github.com/tailscale/tailcat"
)

const maxDatagram = tailcat.MaxUDPPayload

type protocol string

const (
	protoUDP  protocol = "udp"
	protoTCP  protocol = "tcp"
	protoBoth protocol = "both"
)

func (p protocol) valid() bool  { return p == protoUDP || p == protoTCP || p == protoBoth }
func (p protocol) hasTCP() bool { return p == protoTCP || p == protoBoth }
func (p protocol) hasUDP() bool { return p == protoUDP || p == protoBoth }

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	if err := run(os.Args[1:]); err != nil {
		log.Printf("fatal: %v", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 2 && args[0] == "resolve-domain" {
		code, err := invitedns.Resolve(context.Background(), args[1])
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(os.Stdout, code)
		return err
	}
	if len(args) < 1 || (args[0] != "host" && args[0] != "join") {
		return errors.New("usage: helper host|join --port PORT --protocol tcp|udp|both --target auto|LOCAL_IP --ready-file PATH --stats-file PATH [--addr-file PATH]")
	}
	mode := args[0]
	fs := flag.NewFlagSet(mode, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	portText := fs.String("port", "", "Remote service ports (e.g. 25566,25567-25568)")
	proto := fs.String("protocol", "udp", "tcp, udp, or both")
	target := fs.String("target", "auto", "host destination: assigned IPv4, loopback, or auto")
	readyFile := fs.String("ready-file", "", "Private readiness JSON path")
	statsFile := fs.String("stats-file", "", "Private diagnostics JSON path")
	addressFile := fs.String("addr-file", "", "Private Tailcat address file for joining")
	localPort := fs.String("local-port", "", "Preferred local ports (single base or equal-length list)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	p := protocol(*proto)
	ports, parseErr := portset.Parse(*portText)
	if fs.NArg() != 0 || parseErr != nil || !p.valid() || *readyFile == "" || *statsFile == "" {
		return errors.New("invalid protocol, service port, or ready/stats arguments")
	}
	var addr tailcat.Addr
	if mode == "host" && *localPort != "" {
		return errors.New("--local-port is only valid for joining")
	}
	if mode == "join" {
		if *addressFile == "" || *target != "auto" {
			return errors.New("join requires --addr-file; no target override")
		}
		contents, err := os.ReadFile(*addressFile)
		if err != nil {
			return err
		}
		addr = tailcat.Addr(strings.TrimSpace(string(contents)))
		if _, err := tailcat.ParseAddr(addr); err != nil {
			return fmt.Errorf("invalid Tailcat address: %w", err)
		}
	} else if *addressFile != "" {
		return errors.New("host does not accept --addr-file")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if mode == "host" {
		return host(ctx, ports, p, *target, *readyFile, *statsFile)
	}
	var requested []int
	if strings.TrimSpace(*localPort) != "" {
		requested, parseErr = portset.Parse(*localPort)
		if parseErr != nil {
			return parseErr
		}
	}
	preferred, err := portset.Mapping(ports, requested)
	if err != nil {
		return err
	}
	return join(ctx, addr, ports, preferred, p, *readyFile, *statsFile)
}

func writeReady(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0600)
}

// resolveTarget only permits loopback or an assigned local RFC1918 interface.
// It NEVER permits routing to arbitrary LAN addresses supplied by invite holders.
func resolveTarget(name string) (net.IP, error) {
	if name == "auto" {
		return chosenLocalIP()
	}
	ip := net.ParseIP(name).To4()
	if ip == nil {
		return nil, errors.New("host target must be an IPv4 literal")
	}
	if ip.Equal(net.IPv4(127, 0, 0, 1)) {
		return ip, nil
	}
	if !validPrivateIPv4(ip) {
		return nil, errors.New("host target must be loopback or locally assigned RFC1918 IPv4")
	}
	nics, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	for _, nic := range nics {
		if nic.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := nic.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if x, ok := a.(*net.IPNet); ok && x.IP.Equal(ip) {
				return ip, nil
			}
		}
	}
	return nil, fmt.Errorf("target %s is not assigned to this computer", name)
}

func chosenPorts(ports []int) map[uint16]bool {
	selected := make(map[uint16]bool, len(ports))
	for _, p := range ports {
		selected[uint16(p)] = true
	}
	return selected
}

func onServiceUDP(allowed map[uint16]bool, ip net.IP, stats *packetStats) func(uint16) func(tailcat.ConnPacketConn) {
	return func(requested uint16) func(tailcat.ConnPacketConn) {
		if !allowed[requested] {
			return nil
		}
		return func(remote tailcat.ConnPacketConn) {
			local, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: ip, Port: int(requested)})
			if err != nil {
				log.Printf("service UDP %d unreachable: %v", requested, err)
				_ = remote.Close()
				return
			}
			proxyUDP(remote, local, stats)
		}
	}
}

func host(ctx context.Context, ports []int, proto protocol, target string, readyFile, statsFile string) error {
	ip, err := resolveTarget(target)
	if err != nil {
		return err
	}
	stats := &packetStats{}
	stats.setDetails("host", string(proto), ip.String(), uint16(ports[0]))
	stats.setPorts(ports, nil)
	stats.initPassiveServices(ports, proto)
	stats.setConnection("hosting", "", 0, 0)
	server := &tailcat.Server{}
	allowed := chosenPorts(ports)
	if proto.hasUDP() {
		server.OnUDP = onServiceUDP(allowed, ip, stats)
	}
	if proto.hasTCP() {
		server.OnTCP = onServiceTCP(allowed, ip, stats)
	}
	// OnTCPForward and OnUDPForward are NIL. Never serve arbitrary destinations.
	if err := server.Start(); err != nil {
		return fmt.Errorf("Tailcat server: %w", err)
	}
	defer server.Close()
	if err := stats.publish(ctx, statsFile); err != nil {
		return err
	}
	if err := writeReady(readyFile, map[string]any{
		"address": string(server.TailcatAddr()), "ip": ip.String(), "ports": ports,
	}); err != nil {
		return err
	}
	log.Printf("serving %s ports %s to fixed local target %s; no exit-node access", proto, portset.CSV(ports), ip)
	<-ctx.Done()
	return nil
}

// join reserves local listeners ONCE. Reconnecting Tailcat does not close the
// sockets, change their ports, or invalidate the game connection details.
// Ready means the local listeners are reserved, NOT that the remote peer has
// completed a handshake. The separate phase field makes that explicit.
func join(ctx context.Context, addr tailcat.Addr, ports, preferred []int, proto protocol, readyFile, statsFile string) error {
	ip, err := chosenLocalIP()
	if err != nil {
		return err
	}
	stats := &packetStats{}
	stats.setDetails("join", string(proto), ip.String(), uint16(ports[0]))
	listeners, err := portset.Reserve(ip, preferred, string(proto))
	if err != nil {
		return err
	}
	defer portset.CloseAll(listeners)
	localPorts := portset.LocalPorts(listeners)
	stats.setPorts(ports, localPorts)
	stats.setLocalPolicy(portset.Strategy(preferred, localPorts))
	stats.setService("remote-unverified", nil)
	stats.setConnection("connecting", "", 0, 0)
	if err := stats.publish(ctx, statsFile); err != nil {
		return err
	}
	if err := writeReady(readyFile, map[string]any{
		"ip": ip.String(), "port": localPorts[0], "ports": localPorts,
		"requestedPort": ports[0], "remotePorts": ports,
		"portChanged": portset.CSV(localPorts) != portset.CSV(preferred),
	}); err != nil {
		return err
	}
	log.Printf("reserved local %s %s -> fixed remote %s; strategy=%s", proto,
		portset.CSV(localPorts), portset.CSV(ports), portset.Strategy(preferred, localPorts))

	failures := 0
	for ctx.Err() == nil {
		wasOnline, err := connectAndServe(ctx, addr, ports, proto, ip, listeners, stats)
		if ctx.Err() != nil {
			break
		}
		if wasOnline {
			failures = 0
		} // healthy sessions reset exponential backoff
		failures++
		delay := reliability.Backoff(failures)
		stats.setConnection("reconnecting", err.Error(), failures, int(delay.Seconds()))
		log.Printf("connection lost (%v), retrying in %s while retaining local ports %s", err, delay, portset.CSV(localPorts))
		if !reliability.Wait(ctx, delay) {
			break
		}
	}
	stats.setConnection("stopped", "", 0, 0)
	return nil
}

// A single Tailcat generation can be replaced without disturbing its local
// listeners. All UDP readers have exited before a subsequent generation starts.
func connectAndServe(parent context.Context, addr tailcat.Addr, ports []int, proto protocol, ip net.IP, listeners []portset.Bound, stats *packetStats) (bool, error) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	client := tailcat.NewClient(addr)
	closeClient := sync.OnceFunc(func() { _ = client.Close() })
	defer closeClient()
	dialCtx, stop := context.WithTimeout(ctx, 25*time.Second)
	_, err := client.Ping(dialCtx)
	stop()
	if err != nil {
		return false, fmt.Errorf("handshake: %w", err)
	}

	remotes := make([]tailcat.ConnPacketConn, len(ports))
	var cleanup sync.Once
	closeRemotes := func() {
		cleanup.Do(func() {
			for _, r := range remotes {
				if r != nil {
					_ = r.Close()
				}
			}
		})
	}
	defer closeRemotes()
	if proto.hasUDP() {
		for i, p := range ports {
			dialCtx, stop = context.WithTimeout(ctx, 20*time.Second)
			remotes[i], err = client.DialUDPPort(dialCtx, uint16(p))
			stop()
			if err != nil {
				return false, fmt.Errorf("UDP service %d: %w", p, err)
			}
		}
	}
	stats.setConnection("online", "", 0, 0)
	// The joiner can prove the P2P tunnel is reachable, but cannot prove a UDP
	// game server is listening without a protocol-specific response.
	failures := make(chan error, len(ports)*3+1)
	var wg sync.WaitGroup
	for i, p := range ports {
		b := listeners[i]
		if b.TCP != nil {
			wg.Add(1)
			go func() { defer wg.Done(); serveLocalTCP(ctx, b.TCP, ip, client, uint16(p), stats, failures) }()
		}
		if b.UDP != nil {
			wg.Add(1)
			stats.activeFlows.Add(1)
			go func() {
				defer wg.Done()
				defer stats.activeFlows.Add(-1)
				relayLocalUDP(ctx, b.UDP, remotes[i], ip, stats, failures)
			}()
		}
	}
	wg.Add(1)
	go func() { defer wg.Done(); measurePath(ctx, client, stats, failures) }()
	select {
	case <-parent.Done():
		err = nil
	case err = <-failures:
	}
	cancel()
	// These connections unblock remote reads/writes; listeners stay bound.
	closeRemotes()
	closeClient()
	wg.Wait()
	if parent.Err() != nil {
		return true, nil
	}
	if err == nil {
		return true, errors.New("connection ended")
	}
	return true, err
}

// Path probes detect loss of Tailcat's transport, not whether an application
// on a UDP port has answered. Transient timeouts require three consecutive
// failures before a full client reconnect.
func measurePath(ctx context.Context, client *tailcat.Client, stats *packetStats, failures chan<- error) {
	ticker := time.NewTicker(reliability.ProbeInterval)
	defer ticker.Stop()
	var consecutive reliability.CountFailures
	for {
		pingCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
		result, err := client.DiscoPing(pingCtx)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			stats.setPath("unknown", "", "", 0, err.Error())
		} else {
			method := "derp"
			if result.Endpoint != "" {
				method = "direct"
			}
			stats.setPath(method, result.Endpoint, result.DERPRegionCode, result.LatencySeconds*1000, "")
		}
		// Path discovery can fail while the actual game tunnel still works.
		// Incoming application traffic is stronger evidence of liveness.
		probeErr := err
		if err != nil && reliability.RecentIncomingTraffic(stats.lastRXUnix.Load(), time.Now()) {
			probeErr = nil
		}
		if consecutive.Observe(probeErr) {
			reportFailure(ctx, failures, fmt.Errorf("connectivity probes failed repeatedly: %w", err))
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func reportFailure(ctx context.Context, out chan<- error, err error) {
	if err == nil || ctx.Err() != nil {
		return
	}
	select {
	case out <- err:
	case <-ctx.Done():
	default: // avoid blocking or leaking a worker if another failure won the race
	}
}

// Passive service diagnostics: opening speculative TCP connections to game
// servers can consume player slots or crash implementations which do not
// tolerate a client disconnecting before completing the game handshake.
// Only real forwarded client traffic may change an availability observation.
// In particular, do not call net.Dial here, including during startup.
func (s *packetStats) initPassiveServices(ports []int, proto protocol) {
	entries := make([]serviceProbe, 0, len(ports)*2)
	for _, p := range ports {
		if proto.hasTCP() {
			entries = append(entries, serviceProbe{p, "tcp", "unverified"})
		}
		if proto.hasUDP() {
			entries = append(entries, serviceProbe{p, "udp", "unverified"})
		}
	}
	s.setService("unverified", entries)
}
