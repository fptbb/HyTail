// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"fmt"
	"github.com/tailscale/tailcat"
	"io"
	"log"
	"net"
	"strconv"
	"sync"
	"time"
)

// TCP destinations are fixed locally. Only the configured single port exists
// inside the Tailcat server; OnTCPForward is intentionally never registered.
func onServiceTCP(allowed map[uint16]bool, ip net.IP, stats *packetStats) func(uint16) func(net.Conn) {
	return func(requested uint16) func(net.Conn) {
		if !allowed[requested] {
			return nil
		}
		return func(remote net.Conn) {
			local, err := net.DialTimeout("tcp4", net.JoinHostPort(ip.String(), itoa(requested)), 10*time.Second)
			if err != nil {
				stats.recordServiceTCP(int(requested), false)
				log.Printf("service TCP unreachable: %v", err)
				_ = remote.Close()
				return
			}
			stats.recordServiceTCP(int(requested), true)
			stats.activeFlows.Add(1)
			defer stats.activeFlows.Add(-1)
			tailcat.ProxyConns(countedLocalConn{Conn: local, stats: stats}, remote)
		}
	}
}

func itoa(p uint16) string { return strconv.Itoa(int(p)) }

// Counts TCP bytes at the local socket, leaving the underlying Tailcat TCP
// connection unwrapped so ProxyConns can perform proper FIN/ACK draining.
type countedLocalConn struct {
	net.Conn
	stats *packetStats
}

func (c countedLocalConn) Read(b []byte) (int, error) {
	n, e := c.Conn.Read(b)
	c.stats.recordTX(n)
	return n, e
}
func (c countedLocalConn) Write(b []byte) (int, error) {
	n, e := c.Conn.Write(b)
	c.stats.recordRX(n)
	return n, e
}

// ProxyConns attempts to half-close its input with CloseWrite. Preserve this
// behavior on the wrapped local TCP socket for graceful stream termination.
func (c countedLocalConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return c.Conn.Close()
}

func serveLocalTCP(ctx context.Context, ln *net.TCPListener, ip net.IP, client *tailcat.Client, port uint16, stats *packetStats, fail chan<- error) {
	var active sync.WaitGroup
	defer active.Wait()
	for {
		if ctx.Err() != nil {
			return
		}
		_ = ln.SetDeadline(time.Now().Add(400 * time.Millisecond))
		local, err := ln.AcceptTCP()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			reportFailure(ctx, fail, fmt.Errorf("local TCP accept: %w", err))
			return
		}
		if !local.RemoteAddr().(*net.TCPAddr).IP.Equal(ip) {
			_ = local.Close()
			continue
		}
		active.Add(1)
		go func(local *net.TCPConn) {
			defer active.Done()
			stopClose := context.AfterFunc(ctx, func() { _ = local.Close() })
			defer stopClose()
			defer local.Close()
			dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			remote, err := client.DialTCPPort(dialCtx, port)
			cancel()
			if err != nil {
				log.Printf("TCP dial: %v", err)
				return
			}
			stats.activeFlows.Add(1)
			defer stats.activeFlows.Add(-1)
			tailcat.ProxyConns(countedLocalConn{Conn: local, stats: stats}, remote)
		}(local)
	}
}

// A single UDP endpoint per local bridge instance. Preserve datagram boundaries
// and permit only traffic sourced from the joining machine's selected private IP.
// relayLocalUDP is scoped to one remote Tailcat generation. Its two readers
// exit on cancellation, without closing the local UDP listener. This permits
// reconnecting the tunnel while keeping the original game-side port reserved.
func relayLocalUDP(ctx context.Context, listener *net.UDPConn, remote tailcat.ConnPacketConn, localIP net.IP, stats *packetStats, fail chan<- error) {
	type replyPeer struct{ addr *net.UDPAddr }
	peers := make(chan replyPeer, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 65535)
		for ctx.Err() == nil {
			_ = listener.SetReadDeadline(time.Now().Add(350 * time.Millisecond))
			n, from, err := listener.ReadFromUDP(buf)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				reportFailure(ctx, fail, fmt.Errorf("local UDP receive: %w", err))
				return
			}
			if !from.IP.Equal(localIP) || n > maxDatagram {
				continue
			}
			select {
			case peers <- replyPeer{from}:
			default:
			}
			if _, err := remote.Write(buf[:n]); err != nil {
				reportFailure(ctx, fail, fmt.Errorf("UDP tunnel send: %w", err))
				return
			}
			stats.recordTX(n)
		}
	}()
	defer wg.Wait()
	buf := make([]byte, 65535)
	var latest *net.UDPAddr
	for ctx.Err() == nil {
		n, err := remote.Read(buf)
		if err != nil {
			reportFailure(ctx, fail, fmt.Errorf("UDP tunnel receive: %w", err))
			return
		}
		if n > maxDatagram {
			continue
		}
		for {
			select {
			case peer := <-peers:
				latest = peer.addr
			default:
				goto send
			}
		}
	send:
		if latest != nil {
			if _, err := listener.WriteToUDP(buf[:n], latest); err != nil {
				reportFailure(ctx, fail, fmt.Errorf("local UDP send: %w", err))
				return
			}
			stats.recordRX(n)
		}
	}
}

func proxyUDP(remote tailcat.ConnPacketConn, local *net.UDPConn, stats *packetStats) {
	stats.activeFlows.Add(1)
	defer stats.activeFlows.Add(-1)
	defer remote.Close()
	defer local.Close()
	done := make(chan struct{}, 2)
	go func() {
		defer func() { done <- struct{}{} }()
		b := make([]byte, 65535)
		for {
			n, err := remote.Read(b)
			if err != nil {
				return
			}
			if n > maxDatagram {
				continue
			}
			if _, err := local.Write(b[:n]); err != nil {
				return
			}
			stats.recordRX(n)
		}
	}()
	go func() {
		defer func() { done <- struct{}{} }()
		b := make([]byte, 65535)
		for {
			n, err := local.Read(b)
			if err != nil {
				return
			}
			if n > maxDatagram {
				continue
			}
			if _, err := remote.Write(b[:n]); err != nil {
				return
			}
			stats.recordTX(n)
		}
	}()
	<-done
	_ = remote.Close()
	_ = local.Close()
	<-done
}

// Keep the compiler honest about io net.Conn compatibility.
var _ io.Reader = (countedLocalConn{})
