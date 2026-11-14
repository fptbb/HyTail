// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

type packetStats struct {
	txPackets         atomic.Uint64 // successful write calls on tunneled connection
	rxPackets         atomic.Uint64 // successful read calls on tunneled connection
	txBytes           atomic.Uint64
	rxBytes           atomic.Uint64
	activeFlows       atomic.Int64
	lastActivity      atomic.Int64
	lastRXUnix        atomic.Int64
	began             time.Time
	mu                sync.RWMutex
	mode              string
	protocol          string
	targetIP          string
	remotePort        uint16
	remotePorts       []int
	localPorts        []int
	path              string
	endpoint          string
	region            string
	latencyMS         float64
	pathError         string
	phase             string
	lastError         string
	retryAttempt      int
	retrySeconds      int
	retryUntil        time.Time
	lastConnectedUnix int64
	serviceStatus     string
	servicePorts      []serviceProbe
	localPortPolicy   string
	rxAtConnect       uint64
}

type serviceProbe struct {
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	Status   string `json:"status"`
}

type statsSnapshot struct {
	Mode              string         `json:"mode"`
	Protocol          string         `json:"protocol"`
	TargetIP          string         `json:"target_ip"`
	RemotePort        uint16         `json:"remote_port"`
	RemotePorts       []int          `json:"remote_ports,omitempty"`
	LocalPorts        []int          `json:"local_ports,omitempty"`
	TxPackets         uint64         `json:"tunnel_tx_packets"`
	RxPackets         uint64         `json:"tunnel_rx_packets"`
	TxBytes           uint64         `json:"tunnel_tx_bytes"`
	RxBytes           uint64         `json:"tunnel_rx_bytes"`
	ActiveFlows       int64          `json:"active_flows"`
	LastActivityUnix  int64          `json:"last_activity_unix"`
	UptimeSeconds     int64          `json:"uptime_seconds"`
	Path              string         `json:"path"`
	DirectEndpoint    string         `json:"direct_endpoint"`
	DERPRegion        string         `json:"derp_region"`
	LatencyMS         float64        `json:"latency_ms"`
	PathError         string         `json:"path_error"`
	Phase             string         `json:"phase"`
	LastError         string         `json:"last_error"`
	RetryAttempt      int            `json:"retry_attempt"`
	RetrySeconds      int            `json:"retry_seconds"`
	LastConnectedUnix int64          `json:"last_connected_unix"`
	ServiceStatus     string         `json:"service_status"`
	ServicePorts      []serviceProbe `json:"service_ports,omitempty"`
	LocalPortPolicy   string         `json:"local_port_policy"`
}

func (s *packetStats) setDetails(mode, proto, ip string, port uint16) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mode = mode
	s.protocol = proto
	s.targetIP = ip
	s.remotePort = port
	s.began = time.Now()
	s.path = "unknown"
	s.phase = "starting"
	s.serviceStatus = "not_probed"
}
func (s *packetStats) setPorts(remote, local []int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.remotePorts = append([]int(nil), remote...)
	s.localPorts = append([]int(nil), local...)
}
func (s *packetStats) setPath(method, endpoint, region string, ms float64, errorText string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.path = method
	s.endpoint = endpoint
	s.region = region
	s.latencyMS = ms
	s.pathError = errorText
}
func (s *packetStats) setConnection(phase, message string, attempt int, retrySeconds int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phase, s.lastError, s.retryAttempt, s.retrySeconds = phase, message, attempt, retrySeconds
	s.retryUntil = time.Time{}
	if phase == "reconnecting" {
		s.retryUntil = time.Now().Add(time.Duration(retrySeconds) * time.Second)
	}
	if phase != "online" && s.mode == "join" {
		s.serviceStatus = "remote-unverified"
	}
	if phase == "online" {
		s.rxAtConnect = s.rxBytes.Load()
		s.lastConnectedUnix = time.Now().Unix()
		s.pathError = ""
	}
	if phase == "reconnecting" || phase == "connecting" {
		s.path = "unknown"
		s.endpoint = ""
		s.region = ""
		s.latencyMS = 0
	}
}
func (s *packetStats) setLocalPolicy(strategy string) {
	s.mu.Lock()
	s.localPortPolicy = strategy
	s.mu.Unlock()
}
func (s *packetStats) setService(status string, probes []serviceProbe) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.serviceStatus = status
	s.servicePorts = append([]serviceProbe(nil), probes...)
}

// recordServiceTCP observes ONLY an actual Tailcat-forwarded connection.
// No socket is created for diagnostics. A successful TCP dial means the game
// accepted a transport connection, not that the application handshake worked.
func (s *packetStats) recordServiceTCP(port int, connected bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.servicePorts {
		entry := &s.servicePorts[i]
		if entry.Port == port && entry.Protocol == "tcp" {
			if connected {
				entry.Status = "tcp-accepted"
			} else {
				entry.Status = "unreachable"
			}
		}
	}
	s.serviceStatus = aggregateServiceStatus(s.servicePorts)
}

func aggregateServiceStatus(entries []serviceProbe) string {
	hasReply, hasFailure, hasUDP := false, false, false
	for _, entry := range entries {
		if entry.Protocol == "udp" {
			hasUDP = true
		}
		if entry.Protocol == "tcp" {
			if entry.Status == "tcp-accepted" {
				hasReply = true
			}
			if entry.Status == "unreachable" {
				hasFailure = true
			}
		}
	}
	if hasFailure {
		return "tcp-unreachable"
	}
	if hasReply && hasUDP {
		return "tcp-accepted-udp-unverified"
	}
	if hasReply {
		return "tcp-accepted"
	}
	return "unverified"
}

func (s *packetStats) recordTX(n int) {
	if n <= 0 {
		return
	}
	s.txPackets.Add(1)
	s.txBytes.Add(uint64(n))
	s.lastActivity.Store(time.Now().Unix())
}
func (s *packetStats) recordRX(n int) {
	if n <= 0 {
		return
	}
	s.rxPackets.Add(1)
	s.rxBytes.Add(uint64(n))
	s.lastActivity.Store(time.Now().Unix())
	s.lastRXUnix.Store(time.Now().Unix())
}
func (s *packetStats) snapshot(_ string) statsSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	uptime := int64(0)
	if !s.began.IsZero() {
		uptime = int64(time.Since(s.began).Seconds())
	}
	serviceStatus := s.serviceStatus
	remaining := s.retrySeconds
	if !s.retryUntil.IsZero() {
		left := time.Until(s.retryUntil)
		if left <= 0 {
			remaining = 0
		} else {
			remaining = int((left + time.Second - 1) / time.Second)
		}
	}
	if s.mode == "join" && s.phase == "online" && s.rxBytes.Load() > s.rxAtConnect {
		serviceStatus = "application-replied"
	}
	return statsSnapshot{Mode: s.mode, Protocol: s.protocol, TargetIP: s.targetIP, RemotePort: s.remotePort, RemotePorts: append([]int(nil), s.remotePorts...), LocalPorts: append([]int(nil), s.localPorts...),
		TxPackets: s.txPackets.Load(), RxPackets: s.rxPackets.Load(), TxBytes: s.txBytes.Load(), RxBytes: s.rxBytes.Load(),
		ActiveFlows: s.activeFlows.Load(), LastActivityUnix: s.lastActivity.Load(), UptimeSeconds: uptime,
		Path: s.path, DirectEndpoint: s.endpoint, DERPRegion: s.region, LatencyMS: s.latencyMS, PathError: s.pathError,
		Phase: s.phase, LastError: s.lastError, RetryAttempt: s.retryAttempt, RetrySeconds: remaining, LastConnectedUnix: s.lastConnectedUnix, ServiceStatus: serviceStatus, ServicePorts: append([]serviceProbe(nil), s.servicePorts...), LocalPortPolicy: s.localPortPolicy}
}
func saveStats(path string, value statsSnapshot) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
		return err
	}
	return nil
}
func (s *packetStats) publish(ctx context.Context, path string) error {
	if err := saveStats(path, s.snapshot("")); err != nil {
		return fmt.Errorf("write diagnostics: %w", err)
	}
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = saveStats(path, s.snapshot(""))
			}
		}
	}()
	return nil
}
