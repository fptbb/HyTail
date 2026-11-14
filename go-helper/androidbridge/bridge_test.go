package androidbridge

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/json"
	"example.com/hytail-helper/portset"
	"io"
	"net"
	"strings"
	"testing"
)

func TestGenericFormatMatchesDesktop(t *testing.T) {
	v := invite{Version: 1, Address: "tcMock", RemotePort: 25565, Protocol: "tcp", Name: "Minecraft", Preset: "minecraft", ExpiresAt: "2099-01-01T00:00:00Z"}
	encoded, e := pack(v)
	if e != nil {
		t.Fatal(e)
	}
	if strings.ContainsAny(encoded, "=+/") {
		t.Fatal("not unpadded base64url")
	}
	b, e := unpack("TCBR1:"+encoded, "TCBR1:")
	if e != nil {
		t.Fatal(e)
	}
	var fields map[string]any
	if e = json.Unmarshal(b, &fields); e != nil {
		t.Fatal(e)
	}
	if fields["RemotePort"] != float64(25565) || fields["Protocol"] != "tcp" {
		t.Fatalf("unexpected fields: %v", fields)
	}
}
func TestHytaleRewriting(t *testing.T) {
	original := json.RawMessage(`{"Candidates":[{"Address":"192.168.1.2","Port":5520}],"Ice":{"SessionId":"a"},"HostName":"Test"}`)
	code, e := hytaleJoinCode(original, "127.0.0.1", 5522)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := base64.StdEncoding.DecodeString(code)
	if e != nil {
		t.Fatal(e)
	}
	r := flate.NewReader(bytes.NewReader(raw))
	defer r.Close()
	decoded, e := io.ReadAll(r)
	if e != nil {
		t.Fatal(e)
	}
	var payload map[string]any
	if e = json.Unmarshal(decoded, &payload); e != nil {
		t.Fatal(e)
	}
	if _, ok := payload["Ice"]; ok {
		t.Fatal("ICE was not removed")
	}
	candidates, ok := payload["Candidates"].([]any)
	if !ok || len(candidates) != 1 {
		t.Fatal("candidates not rewritten")
	}
	candidate := candidates[0].(map[string]any)
	if candidate["Address"] != "127.0.0.1" || candidate["Port"] != float64(5522) {
		t.Fatal("local endpoint not rewritten")
	}
}
func TestLocalPortFallback(t *testing.T) {
	ip := net.IPv4(127, 0, 0, 1)
	first, e := net.ListenTCP("tcp4", &net.TCPAddr{IP: ip, Port: 0})
	if e != nil {
		t.Fatal(e)
	}
	defer first.Close()
	preferred := first.Addr().(*net.TCPAddr).Port
	group, e := portset.Reserve(ip, []int{preferred}, "tcp")
	if e != nil {
		t.Fatal(e)
	}
	defer portset.CloseAll(group)
	if group[0].TCP == nil || group[0].Port == preferred {
		t.Fatal("fallback failed")
	}
	if portset.Strategy([]int{preferred}, portset.LocalPorts(group)) == "invalid" {
		t.Fatal("invalid local remap")
	}
}

func TestDestinationRestrictions(t *testing.T) {
	if _, e := hostIP("8.8.8.8"); e == nil {
		t.Fatal("arbitrary internet destination allowed")
	}
	if _, e := hostIP("127.0.0.1"); e != nil {
		t.Fatal(e)
	}
	if _, e := parsePort("65536"); e == nil {
		t.Fatal("invalid port allowed")
	}
}

func TestMultiPortInviteFields(t *testing.T) {
	v := invite{Version: 1, Address: "tcMock", RemotePort: 25566, RemotePorts: []int{25566, 25567, 25568}, Protocol: "both", Name: "Game", Preset: "generic", ExpiresAt: "2099-01-01T00:00:00Z"}
	encoded, e := pack(v)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := unpack("TCBR1:"+encoded, "TCBR1:")
	if e != nil {
		t.Fatal(e)
	}
	var decoded invite
	if e = json.Unmarshal(raw, &decoded); e != nil {
		t.Fatal(e)
	}
	if decoded.RemotePort != 25566 || len(decoded.RemotePorts) != 3 || decoded.RemotePorts[2] != 25568 {
		t.Fatalf("bad multiport invite: %+v", decoded)
	}
}

// Unlike the pack/unpack-only tests, detection validates the embedded Tailcat
// address. Use a deterministic, parseable address from Tailcat v0.7.0's
// TestAddr ("just_key"). This address
// identifies no live server and is used solely for offline parser tests.
const parseableTestTailcatAddress = "tcoWFwWCAAAQIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAHw"

// Inline invite detection must never do DNS lookups or accept partial codes.
func TestDetectInvitePreset(t *testing.T) {
	for _, tt := range []struct{ preset, proto, want string }{
		{"minecraft", "tcp", "minecraft-java"},
		{"minecraft", "both", "minecraft-bedrock"},
		{"minecraft", "udp", "minecraft-bedrock"},
		{"TERRARIA", "tcp", "terraria"},
		{"unknown-future-game", "udp", "generic"},
		{"generic", "both", "generic"},
	} {
		code, err := pack(invite{
			Version: 1, Address: parseableTestTailcatAddress, RemotePort: 7777,
			Protocol: tt.proto, Name: "server", Preset: tt.preset,
			ExpiresAt: "2099-01-01T00:00:00Z",
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := DetectInvitePreset("TCBR1:" + code); got != tt.want {
			t.Fatalf("%s/%s: got %s, want %s", tt.preset, tt.proto, got, tt.want)
		}
	}
	hc, err := pack(invite{Version: 1, Address: parseableTestTailcatAddress, RemotePort: 5520, Hytale: json.RawMessage(`{"Candidates":[]}`)})
	if err != nil {
		t.Fatal(err)
	}
	if got := DetectInvitePreset("HTBR1:" + hc); got != "hytale" {
		t.Fatalf("Hytale detected as %q", got)
	}
	// The envelope can be well-formed while its Tailcat address is invalid.
	invalidAddrCode, err := pack(invite{Version: 1, Address: "tcMock", RemotePort: 7777, Protocol: "tcp", Preset: "minecraft"})
	if err != nil {
		t.Fatal(err)
	}
	if got := DetectInvitePreset("TCBR1:" + invalidAddrCode); got != "" {
		t.Fatalf("invalid embedded Tailcat address was accepted: %q", got)
	}
	for _, code := range []string{"games.example.com", "TCBR1:garbage", "HTBR1:garbage", ""} {
		if got := DetectInvitePreset(code); got != "" {
			t.Fatalf("unexpected detection: %q", got)
		}
	}
}

func TestPassiveServiceDiagnostics(t *testing.T) {
	s := &session{}
	s.initPassiveService([]int{7777, 7778}, "both")
	v := s.get()
	if v.ServiceStatus != "unverified" || len(v.ServicePorts) != 4 {
		t.Fatalf("unexpected initial state: %+v", v)
	}
	s.observeServiceTCP(7777, true)
	if v = s.get(); v.ServiceStatus != "tcp-accepted-udp-unverified" {
		t.Fatalf("missing observed connection: %+v", v)
	}
	s.observeServiceTCP(7778, false)
	if v = s.get(); v.ServiceStatus != "tcp-unreachable" {
		t.Fatalf("missing observed rejection: %+v", v)
	}
}
