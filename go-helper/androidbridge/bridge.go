// SPDX-License-Identifier: MIT
// Package androidbridge embeds Tailcat directly in an Android .aar via gomobile.
// There is no executable extraction, VPN permission, exit node, or arbitrary routing.
package androidbridge

import (
	"bytes"
	"compress/flate"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"example.com/hytail-helper/invitedns"
	"example.com/hytail-helper/portset"
	"example.com/hytail-helper/reliability"
	"github.com/tailscale/tailcat"
)

const maxInvite = 65536

type invite struct {
	Version     int             `json:"Version"`
	Address     string          `json:"TailcatAddress"`
	RemotePort  int             `json:"RemotePort"`
	RemotePorts []int           `json:"RemotePorts,omitempty"`
	Protocol    string          `json:"Protocol"`
	Name        string          `json:"Name"`
	Preset      string          `json:"Preset"`
	ExpiresAt   string          `json:"ExpiresAt"`
	TargetIP    string          `json:"TargetIP,omitempty"`
	Hytale      json.RawMessage `json:"Hytale,omitempty"`
}

type serviceProbe struct {
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	Status   string `json:"status"`
}
type state struct {
	Phase             string         `json:"phase,omitempty"`
	LastError         string         `json:"lastError,omitempty"`
	RetryAttempt      int            `json:"retryAttempt,omitempty"`
	RetrySeconds      int            `json:"retrySeconds,omitempty"`
	LastConnectedUnix int64          `json:"lastConnectedUnix,omitempty"`
	ServiceStatus     string         `json:"serviceStatus,omitempty"`
	ServicePorts      []serviceProbe `json:"servicePorts,omitempty"`
	LocalPortPolicy   string         `json:"localPortPolicy,omitempty"`

	Status       string  `json:"status"`
	Error        string  `json:"error,omitempty"`
	Role         string  `json:"role,omitempty"`
	Preset       string  `json:"preset,omitempty"`
	Protocol     string  `json:"protocol,omitempty"`
	Target       string  `json:"target,omitempty"`
	Port         int     `json:"port,omitempty"`
	Ports        []int   `json:"ports,omitempty"`
	LocalAddress string  `json:"localAddress,omitempty"`
	LocalPort    int     `json:"localPort,omitempty"`
	LocalPorts   []int   `json:"localPorts,omitempty"`
	Invite       string  `json:"invite,omitempty"`
	Result       string  `json:"result,omitempty"`
	Path         string  `json:"path,omitempty"`
	Endpoint     string  `json:"endpoint,omitempty"`
	Region       string  `json:"region,omitempty"`
	LatencyMS    float64 `json:"latencyMs,omitempty"`
	TxBytes      uint64  `json:"txBytes"`
	RxBytes      uint64  `json:"rxBytes"`
	TxPackets    uint64  `json:"txPackets"`
	RxPackets    uint64  `json:"rxPackets"`
	Active       int64   `json:"activeFlows"`
	Uptime       int64   `json:"uptimeSeconds"`
}

type session struct {
	networkChange chan struct{}
	mu            sync.Mutex
	ctx           context.Context
	cancel        context.CancelFunc
	server        *tailcat.Server
	client        *tailcat.Client
	clientCtx     context.Context
	listeners     []portset.Bound
	remotes       []tailcat.ConnPacketConn
	details       state
	since         time.Time
	retryUntil    time.Time
	txBytes       atomic.Uint64
	rxBytes       atomic.Uint64
	rxAtConnect   atomic.Uint64
	txPackets     atomic.Uint64
	rxPackets     atomic.Uint64
	flows         atomic.Int64
	lastRXUnix    atomic.Int64
}

var active struct {
	sync.Mutex
	current *session
	last    state
}

func init()                            { active.last.Status = "stopped" }
func response(v state) string          { b, _ := json.Marshal(v); return string(b) }
func failure(err error) string         { return response(state{Status: "error", Error: err.Error()}) }
func (s *session) set(fn func(*state)) { s.mu.Lock(); fn(&s.details); s.mu.Unlock() }
func (s *session) get() state {
	s.mu.Lock()
	v := s.details
	if !s.retryUntil.IsZero() {
		left := time.Until(s.retryUntil)
		if left <= 0 {
			v.RetrySeconds = 0
		} else {
			v.RetrySeconds = int((left + time.Second - 1) / time.Second)
		}
	}
	s.mu.Unlock()
	v.TxBytes = s.txBytes.Load()
	v.RxBytes = s.rxBytes.Load()
	v.TxPackets = s.txPackets.Load()
	v.RxPackets = s.rxPackets.Load()
	v.Active = s.flows.Load()
	v.Uptime = int64(time.Since(s.since).Seconds())
	if v.Role == "join" && v.Phase == "online" && v.RxBytes > s.rxAtConnect.Load() {
		v.ServiceStatus = "application-replied"
	}
	return v
}

// NetworkChanged is called from Android's connectivity callback. It wakes
// transport health checks without requiring the app to poll the network.
func NetworkChanged() string {
	active.Lock()
	s := active.current
	active.Unlock()
	if s != nil {
		select {
		case s.networkChange <- struct{}{}:
		default:
		}
	}
	return "ok"
}
func (s *session) setPhase(phase, message string, attempt, seconds int) {
	s.set(func(v *state) {
		v.Phase, v.LastError, v.RetryAttempt, v.RetrySeconds = phase, message, attempt, seconds
		s.retryUntil = time.Time{}
		if phase == "reconnecting" {
			s.retryUntil = time.Now().Add(time.Duration(seconds) * time.Second)
		}
		if phase != "online" && v.Role == "join" {
			v.ServiceStatus = "remote-unverified"
		}
		if phase == "online" {
			s.rxAtConnect.Store(s.rxBytes.Load())
			v.LastConnectedUnix = time.Now().Unix()
			v.Error = ""
		}
		if phase == "connecting" || phase == "reconnecting" {
			v.Path = "unknown"
			v.Endpoint = ""
			v.Region = ""
			v.LatencyMS = 0
		}
	})
}

// initPassiveService never probes a game port; even a no-data TCP connect
// can crash a server which expects a complete application handshake.
func (s *session) initPassiveService(ports []int, proto string) {
	results := make([]serviceProbe, 0, len(ports)*2)
	for _, p := range ports {
		if hasTCP(proto) {
			results = append(results, serviceProbe{p, "tcp", "unverified"})
		}
		if hasUDP(proto) {
			results = append(results, serviceProbe{p, "udp", "unverified"})
		}
	}
	s.set(func(v *state) {
		v.ServiceStatus = "unverified"
		v.ServicePorts = results
	})
}

// observeServiceTCP updates diagnostics only for genuine inbound bridge flows.
func (s *session) observeServiceTCP(port int, connected bool) {
	s.set(func(v *state) {
		for i := range v.ServicePorts {
			p := &v.ServicePorts[i]
			if p.Port == port && p.Protocol == "tcp" {
				if connected {
					p.Status = "tcp-accepted"
				} else {
					p.Status = "unreachable"
				}
			}
		}
		hasSuccess, hasFailure, hasUDP := false, false, false
		for _, p := range v.ServicePorts {
			if p.Protocol == "udp" {
				hasUDP = true
			}
			if p.Protocol == "tcp" {
				if p.Status == "tcp-accepted" {
					hasSuccess = true
				}
				if p.Status == "unreachable" {
					hasFailure = true
				}
			}
		}
		switch {
		case hasFailure:
			v.ServiceStatus = "tcp-unreachable"
		case hasSuccess && hasUDP:
			v.ServiceStatus = "tcp-accepted-udp-unverified"
		case hasSuccess:
			v.ServiceStatus = "tcp-accepted"
		default:
			v.ServiceStatus = "unverified"
		}
	})
}

func reportFailure(ctx context.Context, failures chan<- error, err error) {
	if err == nil || ctx.Err() != nil {
		return
	}
	select {
	case failures <- err:
	case <-ctx.Done():
	default:
	}
}
func (s *session) writeTX(n int) {
	if n > 0 {
		s.txBytes.Add(uint64(n))
		s.txPackets.Add(1)
	}
}
func (s *session) writeRX(n int) {
	if n > 0 {
		s.rxBytes.Add(uint64(n))
		s.rxPackets.Add(1)
		s.lastRXUnix.Store(time.Now().Unix())
	}
}

// Status is safe to call on the UI thread, but network operations run in the service's worker pool.
func Status() string {
	active.Lock()
	s := active.current
	v := active.last
	active.Unlock()
	if s != nil {
		return response(s.get())
	}
	return response(v)
}

func begin(role, preset, proto string, port int) (*session, error) {
	active.Lock()
	defer active.Unlock()
	if active.current != nil {
		return nil, errors.New("bridge already active; stop it first")
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &session{ctx: ctx, cancel: cancel, since: time.Now(), networkChange: make(chan struct{}, 1), details: state{Status: "starting", Phase: "starting", Role: role, Preset: preset, Protocol: proto, Port: port, Path: "unknown"}}
	active.current = s
	return s, nil
}
func failSession(s *session, err error) string {
	s.shutdown()
	v := state{Status: "error", Error: err.Error()}
	active.Lock()
	if active.current == s {
		active.current = nil
		active.last = v
	}
	active.Unlock()
	return response(v)
}
func (s *session) shutdown() {
	s.cancel()
	s.mu.Lock()
	listeners, remotes, cl, sv := s.listeners, s.remotes, s.client, s.server
	s.listeners = nil
	s.remotes = nil
	s.client = nil
	s.clientCtx = nil
	s.server = nil
	s.mu.Unlock()
	portset.CloseAll(listeners)
	// A join generation owns its Tailcat client and UDP remotes. Its
	// supervisor closes them once, after observing cancellation, so we do
	// not race duplicate closes while preserving the local listener lifecycle.
	if s.details.Role != "join" {
		for _, remote := range remotes {
			if remote != nil {
				_ = remote.Close()
			}
		}
		if cl != nil {
			_ = cl.Close()
		}
	}
	if sv != nil {
		_ = sv.Close()
	}
}

// Stop closes the server/client, local sockets and active UDP flow.
func Stop() string {
	active.Lock()
	s := active.current
	active.current = nil
	active.last = state{Status: "stopped"}
	active.Unlock()
	if s != nil {
		s.shutdown()
	}
	return Status()
}

func validProtocol(s string) bool { return s == "tcp" || s == "udp" || s == "both" }
func hasTCP(s string) bool        { return s == "tcp" || s == "both" }
func hasUDP(s string) bool        { return s == "udp" || s == "both" }
func parsePort(s string) (int, error) {
	p, e := strconv.Atoi(strings.TrimSpace(s))
	if e != nil || p < 1 || p > 65535 {
		return 0, errors.New("port must be 1..65535")
	}
	return p, nil
}
func localPrivateIP() (net.IP, error) {
	nics, e := net.Interfaces()
	if e != nil {
		return nil, e
	}
	for _, nic := range nics {
		if nic.Flags&net.FlagUp == 0 || nic.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, e := nic.Addrs()
		if e != nil {
			continue
		}
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := n.IP.To4()
			if ip != nil && rfc1918(ip) {
				return ip, nil
			}
		}
	}
	return nil, errors.New("no assigned RFC1918 IPv4 (use loopback on cellular)")
}
func rfc1918(ip net.IP) bool {
	v := ip.To4()
	return v != nil && (v[0] == 10 || (v[0] == 172 && v[1] >= 16 && v[1] <= 31) || (v[0] == 192 && v[1] == 168))
}
func hostIP(target string) (net.IP, error) {
	target = strings.TrimSpace(target)
	if target == "" || target == "loopback" {
		return net.IPv4(127, 0, 0, 1), nil
	}
	if target == "auto" {
		return localPrivateIP()
	}
	v := net.ParseIP(target).To4()
	if v == nil {
		return nil, errors.New("invalid target IPv4")
	}
	if v.Equal(net.IPv4(127, 0, 0, 1)) {
		return v, nil
	}
	if !rfc1918(v) {
		return nil, errors.New("target must be loopback or assigned private IPv4")
	}
	nics, _ := net.Interfaces()
	for _, nic := range nics {
		addrs, _ := nic.Addrs()
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if ok && n.IP.Equal(v) {
				return v, nil
			}
		}
	}
	return nil, errors.New("target IP must be assigned to this device")
}
func pack(v any) (string, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return "", e
	}
	var buf bytes.Buffer
	z, e := flate.NewWriter(&buf, flate.BestCompression)
	if e != nil {
		return "", e
	}
	if _, e = z.Write(b); e != nil {
		return "", e
	}
	if e = z.Close(); e != nil {
		return "", e
	}
	return base64.RawURLEncoding.EncodeToString(buf.Bytes()), nil
}
func unpack(code, prefix string) ([]byte, error) {
	code = strings.TrimSpace(code)
	if !strings.HasPrefix(code, prefix) || len(code) > maxInvite {
		return nil, errors.New("invalid bridge invitation")
	}
	b, e := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(code, prefix))
	if e != nil {
		return nil, e
	}
	r := flate.NewReader(bytes.NewReader(b))
	defer r.Close()
	out, e := io.ReadAll(io.LimitReader(r, maxInvite+1))
	if e != nil {
		return nil, e
	}
	if len(out) > maxInvite {
		return nil, errors.New("invite too large")
	}
	return out, nil
}
func parseCode(code string) (invite, error) {
	var v invite
	prefix := "TCBR1:"
	if strings.HasPrefix(strings.TrimSpace(code), "HTBR1:") {
		prefix = "HTBR1:"
	}
	b, e := unpack(code, prefix)
	if e != nil {
		return v, e
	}
	if e = json.Unmarshal(b, &v); e != nil {
		return v, e
	}
	if v.Version != 1 || v.RemotePort < 1 || v.RemotePort > 65535 || !strings.HasPrefix(v.Address, "tc") || len(v.Address) > 8192 {
		return v, errors.New("invalid invitation fields")
	}
	if prefix == "HTBR1:" {
		if len(v.RemotePorts) != 0 {
			return v, errors.New("Hytale is single-port only")
		}
		v.Protocol = "udp"
		v.Preset = "hytale"
	} else {
		if !validProtocol(v.Protocol) {
			return v, errors.New("invalid protocol")
		}
		if len(v.RemotePorts) > 0 {
			if len(v.RemotePorts) < 2 || len(v.RemotePorts) > portset.MaxPorts || v.RemotePorts[0] != v.RemotePort {
				return v, errors.New("invalid RemotePorts in invite")
			}
			for i, p := range v.RemotePorts {
				if p < 1 || p > 65535 || (i > 0 && p <= v.RemotePorts[i-1]) {
					return v, errors.New("unsorted/duplicate RemotePorts")
				}
			}
		}
	}
	if v.ExpiresAt != "" {
		t, e := time.Parse(time.RFC3339Nano, v.ExpiresAt)
		if e != nil || time.Now().After(t) {
			return v, errors.New("invitation expired")
		}
	}
	if _, e := tailcat.ParseAddr(tailcat.Addr(v.Address)); e != nil {
		return v, fmt.Errorf("invalid Tailcat address: %w", e)
	}
	return v, nil
}

// DetectInvitePreset reads an inline invitation without DNS or network I/O.
// It is safe to call on paste; invalid/incomplete codes return the empty string.
// Unrecognized service presets are treated as Generic while keeping the
// original invite protocol and remote ports unchanged.
func DetectInvitePreset(code string) string {
	code = strings.TrimSpace(code)
	if !strings.HasPrefix(code, "TCBR1:") && !strings.HasPrefix(code, "HTBR1:") {
		return ""
	}
	v, err := parseCode(code)
	if err != nil {
		return ""
	}
	switch strings.ToLower(v.Preset) {
	case "minecraft":
		if v.Protocol == "tcp" {
			return "minecraft-java"
		}
		return "minecraft-bedrock"
	case "hytale", "terraria":
		return strings.ToLower(v.Preset)
	default:
		return "generic"
	}
}

func decodeOriginalHytale(raw string) (json.RawMessage, int, error) {
	raw = strings.Join(strings.Fields(raw), "")
	if len(raw) > maxInvite {
		return nil, 0, errors.New("Hytale code too long")
	}
	compressed, e := base64.StdEncoding.DecodeString(raw)
	if e != nil {
		compressed, e = base64.RawStdEncoding.DecodeString(raw)
	}
	if e != nil {
		return nil, 0, e
	}
	var b []byte
	rd := flate.NewReader(bytes.NewReader(compressed))
	b, e = io.ReadAll(io.LimitReader(rd, maxInvite+1))
	rd.Close()
	if e != nil {
		return nil, 0, e
	}
	var obj map[string]any
	if e = json.Unmarshal(b, &obj); e != nil {
		return nil, 0, e
	}
	if expiry, ok := obj["ExpiresAt"].(string); ok {
		t, e := time.Parse(time.RFC3339Nano, expiry)
		if e == nil && time.Now().After(t) {
			return nil, 0, errors.New("Hytale code expired")
		}
	}
	arr, ok := obj["Candidates"].([]any)
	if !ok {
		return nil, 0, errors.New("Hytale Candidates missing")
	}
	counts := map[int]int{}
	first := []int{}
	for _, a := range arr {
		m, ok := a.(map[string]any)
		if !ok {
			continue
		}
		f, ok := m["Port"].(float64)
		if !ok {
			continue
		}
		p := int(f)
		if p > 0 && p < 65536 {
			if counts[p] == 0 {
				first = append(first, p)
			}
			counts[p]++
		}
	}
	var chosen, n int
	for _, p := range first {
		if counts[p] > n {
			chosen = p
			n = counts[p]
		}
	}
	if chosen == 0 {
		return nil, 0, errors.New("no Hytale candidate port")
	}
	return b, chosen, nil
}
func hytaleJoinCode(raw json.RawMessage, ip string, port int) (string, error) {
	var obj map[string]any
	if e := json.Unmarshal(raw, &obj); e != nil {
		return "", e
	}
	obj["Candidates"] = []any{map[string]any{"Address": ip, "Port": port, "Priority": 1000, "Type": "Host"}}
	delete(obj, "Ice")
	b, e := json.Marshal(obj)
	if e != nil {
		return "", e
	}
	var buf bytes.Buffer
	w, _ := flate.NewWriter(&buf, flate.BestCompression)
	_, e = w.Write(b)
	if e != nil {
		return "", e
	}
	if e = w.Close(); e != nil {
		return "", e
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// StartHost exposes only the explicitly selected ports and protocol through Tailcat.
// Comma lists and inclusive ranges are accepted; Hytale remains single-port.
func StartHost(preset, proto, portText, target, name, originalCode string) string {
	ports, e := portset.Parse(portText)
	var original json.RawMessage
	if preset == "hytale" {
		proto = "udp"
		var p int
		original, p, e = decodeOriginalHytale(originalCode)
		ports = []int{p}
	}
	if e != nil {
		return failure(e)
	}
	if !validProtocol(proto) {
		return failure(errors.New("invalid protocol"))
	}
	ip, e := hostIP(target)
	if e != nil {
		return failure(e)
	}
	s, e := begin("host", preset, proto, ports[0])
	if e != nil {
		return failure(e)
	}
	srv := &tailcat.Server{}
	allowed := make(map[uint16]bool, len(ports))
	for _, p := range ports {
		allowed[uint16(p)] = true
	}
	if hasTCP(proto) {
		srv.OnTCP = func(requested uint16) func(net.Conn) {
			if !allowed[requested] {
				return nil
			}
			return func(remote net.Conn) {
				local, e := net.DialTimeout("tcp4", net.JoinHostPort(ip.String(), strconv.Itoa(int(requested))), 10*time.Second)
				if e != nil {
					s.observeServiceTCP(int(requested), false)
					_ = remote.Close()
					return
				}
				s.observeServiceTCP(int(requested), true)
				s.flows.Add(1)
				defer s.flows.Add(-1)
				proxyTCP(s, remote, local)
			}
		}
	}
	if hasUDP(proto) {
		srv.OnUDP = func(requested uint16) func(tailcat.ConnPacketConn) {
			if !allowed[requested] {
				return nil
			}
			return func(remote tailcat.ConnPacketConn) {
				local, e := net.DialUDP("udp4", nil, &net.UDPAddr{IP: ip, Port: int(requested)})
				if e != nil {
					_ = remote.Close()
					return
				}
				s.flows.Add(1)
				defer s.flows.Add(-1)
				proxyUDP(s, remote, local)
			}
		}
	}
	// No OnTCPForward or OnUDPForward: host-network access is never enabled.
	if e := srv.Start(); e != nil {
		return failSession(s, e)
	}
	s.mu.Lock()
	s.server = srv
	s.mu.Unlock()
	if s.ctx.Err() != nil {
		return failSession(s, errors.New("bridge stopped during startup"))
	}
	v := invite{Version: 1, Address: string(srv.TailcatAddr()), RemotePort: ports[0], Protocol: proto,
		Name: name, Preset: preset, ExpiresAt: time.Now().Add(12 * time.Hour).UTC().Format(time.RFC3339Nano)}
	if len(ports) > 1 {
		v.RemotePorts = append([]int(nil), ports...)
	}
	prefix := "TCBR1:"
	if preset == "hytale" {
		v.TargetIP = ip.String()
		v.Hytale = original
		v.Name = ""
		v.Preset = ""
		v.Protocol = ""
		v.ExpiresAt = ""
		prefix = "HTBR1:"
	}
	encoded, e := pack(v)
	if e != nil {
		return failSession(s, e)
	}
	s.initPassiveService(ports, proto)
	s.set(func(st *state) {
		st.Status = "running"
		st.Phase = "hosting"
		st.Target = ip.String()
		st.Invite = prefix + encoded
		st.Path = "hosting"
		st.Ports = ports
	})
	return response(s.get())
}

func proxyTCP(s *session, remote, local net.Conn) {
	// Keep Tailcat's connection unwrapped for its special FIN-drain handling.
	// Count local side reads and writes, which correspond to tunneled traffic.
	tailcat.ProxyConns(remote, countedLocal{Conn: local, s: s})
}

type countedLocal struct {
	net.Conn
	s *session
}

func (c countedLocal) Read(b []byte) (int, error) {
	n, e := c.Conn.Read(b)
	c.s.writeTX(n)
	return n, e
}
func (c countedLocal) Write(b []byte) (int, error) {
	n, e := c.Conn.Write(b)
	c.s.writeRX(n)
	return n, e
}
func (c countedLocal) CloseWrite() error {
	if x, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return x.CloseWrite()
	}
	return c.Conn.Close()
}
func proxyUDP(s *session, remote tailcat.ConnPacketConn, local *net.UDPConn) {
	defer remote.Close()
	defer local.Close()
	done := make(chan struct{}, 2)
	go func() {
		b := make([]byte, 65535)
		for {
			n, e := remote.Read(b)
			if e != nil {
				break
			}
			if n > tailcat.MaxUDPPayload {
				continue
			}
			if _, e = local.Write(b[:n]); e != nil {
				break
			}
			s.writeRX(n)
		}
		done <- struct{}{}
	}()
	go func() {
		b := make([]byte, 65535)
		for {
			n, e := local.Read(b)
			if e != nil {
				break
			}
			if n > tailcat.MaxUDPPayload {
				continue
			}
			if _, e = remote.Write(b[:n]); e != nil {
				break
			}
			s.writeTX(n)
		}
		done <- struct{}{}
	}()
	<-done
	_ = remote.Close()
	_ = local.Close()
	<-done
}
func sameSubnet(source, host net.IP) bool {
	if host.IsLoopback() {
		return source.IsLoopback()
	}
	nics, _ := net.Interfaces()
	for _, nic := range nics {
		addrs, _ := nic.Addrs()
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if ok && n.IP.Equal(host) {
				return n.Contains(source)
			}
		}
	}
	return false
}

// The local TCP listener is started once and retained for every Tailcat
// generation. While offline, new game connections fail promptly rather than
// waiting forever in the listen backlog.
func acceptTCP(s *session, ln *net.TCPListener, ip net.IP, port int) {
	for s.ctx.Err() == nil {
		_ = ln.SetDeadline(time.Now().Add(400 * time.Millisecond))
		local, err := ln.AcceptTCP()
		if err != nil {
			if s.ctx.Err() != nil {
				return
			}
			if e, ok := err.(net.Error); ok && e.Timeout() {
				continue
			}
			s.setPhase("error", fmt.Sprintf("local TCP listener: %v", err), 0, 0)
			return
		}
		if !sameSubnet(local.RemoteAddr().(*net.TCPAddr).IP, ip) {
			_ = local.Close()
			continue
		}
		s.mu.Lock()
		cl := s.client
		generationCtx := s.clientCtx
		s.mu.Unlock()
		if cl == nil || generationCtx == nil {
			_ = local.Close()
			continue
		}
		go func(local *net.TCPConn, client *tailcat.Client, genCtx context.Context) {
			stopClose := context.AfterFunc(genCtx, func() { _ = local.Close() })
			defer stopClose()
			defer local.Close()
			c, cancel := context.WithTimeout(genCtx, 15*time.Second)
			defer cancel()
			remote, err := client.DialTCPPort(c, uint16(port))
			if err != nil {
				s.set(func(st *state) { st.LastError = fmt.Sprintf("TCP remote port %d: %v", port, err) })
				return
			}
			s.flows.Add(1)
			defer s.flows.Add(-1)
			proxyTCP(s, remote, local)
		}(local, cl, generationCtx)
	}
}

// UDP relay goroutines are explicitly scoped to a remote client generation;
// only the *remote* socket is closed during reconnect. The local UDP port
// remains owned by the original session, and readers are joined before retry.
func relayUDP(ctx context.Context, s *session, local *net.UDPConn, remote tailcat.ConnPacketConn, ip net.IP, failures chan<- error) {
	latest := make(chan *net.UDPAddr, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 65535)
		for ctx.Err() == nil {
			_ = local.SetReadDeadline(time.Now().Add(350 * time.Millisecond))
			n, from, err := local.ReadFromUDP(buf)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				if e, ok := err.(net.Error); ok && e.Timeout() {
					continue
				}
				reportFailure(ctx, failures, fmt.Errorf("UDP local read: %w", err))
				return
			}
			if n > tailcat.MaxUDPPayload || !sameSubnet(from.IP, ip) {
				continue
			}
			select {
			case latest <- from:
			default:
			}
			if _, err = remote.Write(buf[:n]); err != nil {
				reportFailure(ctx, failures, fmt.Errorf("UDP send: %w", err))
				return
			}
			s.writeTX(n)
		}
	}()
	defer wg.Wait()
	buf := make([]byte, 65535)
	var peer *net.UDPAddr
	for ctx.Err() == nil {
		n, err := remote.Read(buf)
		if err != nil {
			reportFailure(ctx, failures, fmt.Errorf("UDP receive: %w", err))
			return
		}
		if n > tailcat.MaxUDPPayload {
			continue
		}
		for {
			select {
			case peer = <-latest:
			default:
				goto send
			}
		}
	send:
		if peer != nil {
			if _, err = local.WriteToUDP(buf[:n], peer); err != nil {
				reportFailure(ctx, failures, fmt.Errorf("UDP reply: %w", err))
				return
			}
			s.writeRX(n)
		}
	}
}

// StartJoin accepts both legacy single-port and multi-port TCBR1 invites.
// The optional local spec is one base port or an equal-size port list.
func StartJoin(code, bindMode, portText string) string {
	resolved, err := invitedns.Resolve(context.Background(), code)
	if err != nil {
		return failure(err)
	}
	v, err := parseCode(resolved)
	if err != nil {
		return failure(err)
	}
	ip := net.IPv4(127, 0, 0, 1)
	if bindMode == "lan" {
		ip, err = localPrivateIP()
		if err != nil {
			return failure(err)
		}
	} else if bindMode != "loopback" {
		return failure(errors.New("invalid bind mode"))
	}
	remotePorts := v.RemotePorts
	if len(remotePorts) == 0 {
		remotePorts = []int{v.RemotePort}
	}
	var localRequest []int
	if strings.TrimSpace(portText) != "" {
		localRequest, err = portset.Parse(portText)
		if err != nil {
			return failure(err)
		}
	}
	preferred, err := portset.Mapping(remotePorts, localRequest)
	if err != nil {
		return failure(err)
	}
	v.Preset = strings.ToLower(v.Preset)
	switch v.Preset {
	case "hytale", "minecraft", "terraria":
	default:
		v.Preset = "generic"
	}
	s, err := begin("join", v.Preset, v.Protocol, remotePorts[0])
	if err != nil {
		return failure(err)
	}
	listeners, err := portset.Reserve(ip, preferred, v.Protocol)
	if err != nil {
		return failSession(s, err)
	}
	s.mu.Lock()
	if s.ctx.Err() != nil {
		s.mu.Unlock()
		portset.CloseAll(listeners)
		return failSession(s, errors.New("bridge stopped during port allocation"))
	}
	s.listeners = listeners
	s.mu.Unlock()
	localPorts := portset.LocalPorts(listeners)
	result := strings.Join(portMappings(ip, localPorts, remotePorts, v.Protocol), "\n")
	if len(remotePorts) == 1 {
		result = net.JoinHostPort(ip.String(), strconv.Itoa(localPorts[0]))
	}
	if v.Preset == "hytale" {
		result, err = hytaleJoinCode(v.Hytale, ip.String(), localPorts[0])
		if err != nil {
			return failSession(s, err)
		}
	}
	s.set(func(st *state) {
		st.Status = "running"   // Foreground service stays alive during reconnection.
		st.Phase = "connecting" // A local listener does not prove the peer is online.
		st.LocalAddress = ip.String()
		st.LocalPort = localPorts[0]
		st.LocalPorts = localPorts
		st.Ports = remotePorts
		st.Result = result
		st.Target = ip.String()
		st.ServiceStatus = "remote-unverified"
		st.LocalPortPolicy = portset.Strategy(preferred, localPorts)
	})
	for i, p := range remotePorts {
		if listeners[i].TCP != nil {
			go acceptTCP(s, listeners[i].TCP, ip, p)
		}
	}
	go manageJoin(s, v, ip, remotePorts, listeners)
	return response(s.get())
}

func portMappings(ip net.IP, local, remote []int, proto string) []string {
	result := make([]string, len(local))
	for i, p := range local {
		result[i] = fmt.Sprintf("%s:%d -> remote %d (%s)", ip, p, remote[i], proto)
	}
	return result
}

// Exponential retries run inside the Android foreground-service lifetime.
// No local socket is closed or rebound until the user explicitly presses Stop.
func manageJoin(s *session, v invite, ip net.IP, ports []int, listeners []portset.Bound) {
	attempt := 0
	for s.ctx.Err() == nil {
		wasOnline, err := runJoinGeneration(s, v, ip, ports, listeners)
		if s.ctx.Err() != nil {
			return
		}
		if wasOnline {
			attempt = 0
		} // Restore fast retries after a healthy session.
		attempt++
		delay := reliability.Backoff(attempt)
		s.setPhase("reconnecting", err.Error(), attempt, int(delay.Seconds()))
		// A network callback can accelerate the next attempt without
		// changing the local sockets or busy-looping on an idle device.
		timer := time.NewTimer(delay)
		select {
		case <-s.ctx.Done():
			timer.Stop()
			return
		case <-s.networkChange:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func runJoinGeneration(s *session, v invite, ip net.IP, ports []int, listeners []portset.Bound) (bool, error) {
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	cl := tailcat.NewClient(tailcat.Addr(v.Address))
	closeClient := sync.OnceFunc(func() { _ = cl.Close() })
	defer closeClient()
	pingCtx, stop := context.WithTimeout(ctx, 25*time.Second)
	_, err := cl.Ping(pingCtx)
	stop()
	if err != nil {
		return false, fmt.Errorf("Tailcat handshake: %w", err)
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
	if hasUDP(v.Protocol) {
		for i, p := range ports {
			c, stop := context.WithTimeout(ctx, 20*time.Second)
			remotes[i], err = cl.DialUDPPort(c, uint16(p))
			stop()
			if err != nil {
				return false, fmt.Errorf("Tailcat UDP %d: %w", p, err)
			}
		}
	}
	s.mu.Lock()
	if s.ctx.Err() != nil {
		s.mu.Unlock()
		return false, s.ctx.Err()
	}
	s.client = cl
	s.clientCtx = ctx
	s.remotes = remotes
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.client == cl {
			s.client = nil
			s.clientCtx = nil
			s.remotes = nil
		}
		s.mu.Unlock()
	}()
	s.setPhase("online", "", 0, 0)
	failures := make(chan error, len(ports)*3+1)
	var wg sync.WaitGroup
	for i := range ports {
		if listeners[i].UDP == nil {
			continue
		}
		s.flows.Add(1)
		wg.Add(1)
		go func(local *net.UDPConn, remote tailcat.ConnPacketConn) {
			defer wg.Done()
			defer s.flows.Add(-1)
			relayUDP(ctx, s, local, remote, ip, failures)
		}(listeners[i].UDP, remotes[i])
	}
	wg.Add(1)
	go func() { defer wg.Done(); monitorJoin(s, ctx, cl, failures) }()
	select {
	case <-s.ctx.Done():
		err = nil
	case err = <-failures:
	}
	cancel()
	closeRemotes()
	closeClient()
	wg.Wait() // Prevent competing readers on the next generation.
	return true, err
}

func monitorJoin(s *session, ctx context.Context, cl *tailcat.Client, failures chan<- error) {
	ticker := time.NewTicker(reliability.ProbeInterval)
	defer ticker.Stop()
	var count reliability.CountFailures
	for {
		pingCtx, stop := context.WithTimeout(ctx, 6*time.Second)
		result, err := cl.DiscoPing(pingCtx)
		stop()
		if ctx.Err() != nil {
			return
		}
		s.set(func(st *state) {
			if err != nil {
				st.Path = "unknown"
				st.Endpoint = ""
				st.Region = ""
				st.LatencyMS = 0
				st.LastError = err.Error()
				return
			}
			if result.Endpoint != "" {
				st.Path = "direct"
			} else {
				st.Path = "derp"
			}
			st.Endpoint = result.Endpoint
			st.Region = result.DERPRegionCode
			st.LatencyMS = result.LatencySeconds * 1000
			st.LastError = ""
		})
		probeErr := err
		if err != nil && reliability.RecentIncomingTraffic(s.lastRXUnix.Load(), time.Now()) {
			probeErr = nil
		}
		if count.Observe(probeErr) {
			reportFailure(ctx, failures, fmt.Errorf("repeated connectivity probe failures: %w", err))
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-s.networkChange:
			continue
		case <-ticker.C:
		}
	}
}

// ValidInvite performs syntactic and expiration checks for the Android UI.
func ValidInvite(code string) bool { _, e := parseCode(code); return e == nil }

// Endpoint returns the active join-side address, if a join session is running.
func Endpoint() string {
	active.Lock()
	s := active.current
	active.Unlock()
	if s == nil {
		return ""
	}
	v := s.get()
	if v.Role != "join" {
		return ""
	}
	return net.JoinHostPort(v.LocalAddress, strconv.Itoa(v.LocalPort))
}
