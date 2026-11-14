package tailcat

import (
	"context"
	"errors"
	"net"
)

const MaxUDPPayload = 9000

type Addr string

func ParseAddr(a Addr) (Addr, error) {
	// Match the real parser's rejection of arbitrary tc-prefixed strings.
	// tcStub is reserved for the offline reconnection tests; the other
	// value is the published valid v0.7.0 Tailcat TestAddr fixture.
	if a != "tcStub" && a != "tcoWFwWCAAAQIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAHw" {
		return "", errors.New("bad addr")
	}
	return a, nil
}

type PingResult struct{}
type DiscoPingResult struct {
	Endpoint, DERPRegionCode string
	LatencySeconds           float64
}
type ConnPacketConn interface {
	Read([]byte) (int, error)
	Write([]byte) (int, error)
	Close() error
}
type Client struct{}

func NewClient(_ Addr) *Client { return &Client{} }
func (c *Client) Close() error { return nil }
func (c *Client) Ping(context.Context) (PingResult, error) {
	return PingResult{}, errors.New("peer unavailable (stub)")
}
func (c *Client) DiscoPing(context.Context) (DiscoPingResult, error) {
	return DiscoPingResult{}, errors.New("peer unavailable (stub)")
}
func (c *Client) DialTCPPort(context.Context, uint16) (net.Conn, error) {
	return nil, errors.New("stub")
}
func (c *Client) DialUDPPort(context.Context, uint16) (ConnPacketConn, error) {
	return nil, errors.New("stub")
}

type Server struct {
	OnUDP func(uint16) func(ConnPacketConn)
	OnTCP func(uint16) func(net.Conn)
}

func (s *Server) Start() error      { return nil }
func (s *Server) Close() error      { return nil }
func (s *Server) TailcatAddr() Addr { return "tcStub" }
func ProxyConns(a, b net.Conn) {
	if a != nil {
		_ = a.Close()
	}
	if b != nil {
		_ = b.Close()
	}
}
