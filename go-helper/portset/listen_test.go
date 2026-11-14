package portset

import (
	"errors"
	"fmt"
	"net"
	"syscall"
	"testing"
)

func TestBusyRecognizesWindowsAddressInUse(t *testing.T) {
	for _, err := range []error{
		syscall.EADDRINUSE,
		fmt.Errorf("listen: %w", syscall.Errno(10048)),
		errors.New("bind: Only one usage of each socket address (protocol/network address/port) is normally permitted."),
		errors.New("bind: address already in use"),
	} {
		if !busy(err) {
			t.Errorf("busy(%v) = false, want true", err)
		}
	}
	if busy(errors.New("permission denied")) {
		t.Fatal("unrelated error classified as address in use")
	}
}

func TestReserveGroup(t *testing.T) {
	ip := net.IPv4(127, 0, 0, 1)
	first, err := Reserve(ip, []int{35010, 35011, 35013}, "both")
	if err != nil {
		t.Fatal(err)
	}
	defer CloseAll(first)
	second, err := Reserve(ip, []int{35010, 35011, 35013}, "both")
	if err != nil {
		t.Fatal(err)
	}
	defer CloseAll(second)
	a, b := LocalPorts(first), LocalPorts(second)
	if b[0] == a[0] {
		t.Fatal("occupied port reused")
	}
	for i := range a {
		if b[i]-a[i] != b[0]-a[0] {
			t.Fatal("group shift is not uniform")
		}
	}
	for _, x := range second {
		if x.TCP == nil || x.UDP == nil {
			t.Fatal("missing listener")
		}
	}
}
func TestRejectBadGroup(t *testing.T) {
	for _, c := range [][]int{{1, 1}, {0}, {65536}, nil} {
		if _, e := Reserve(net.IPv4(127, 0, 0, 1), c, "tcp"); e == nil {
			t.Fatalf("accepted %v", c)
		}
	}
}

func TestStrategyAndStableReservations(t *testing.T) {
	if Strategy([]int{25565, 25566}, []int{25565, 25566}) != "requested" {
		t.Fatal("requested mapping")
	}
	if Strategy([]int{25565, 25566}, []int{25567, 25568}) != "nearby" {
		t.Fatal("nearby mapping")
	}
	if Strategy([]int{25565, 25566}, []int{45000, 45001}) != "ephemeral" {
		t.Fatal("ephemeral mapping")
	}
	if Strategy([]int{25565, 25566}, []int{45000, 45002}) != "invalid" {
		t.Fatal("inconsistent mapping")
	}
	ip := net.IPv4(127, 0, 0, 1)
	listeners, err := Reserve(ip, []int{40122, 40123}, "both")
	if err != nil {
		t.Fatal(err)
	}
	defer CloseAll(listeners)
	original := LocalPorts(listeners)
	// The actual sockets remain bound while remote transport generations
	// are replaced. A concurrent allocator must not steal these ports.
	others, err := Reserve(ip, original, "both")
	if err != nil {
		t.Fatal(err)
	}
	defer CloseAll(others)
	if LocalPorts(others)[0] == original[0] {
		t.Fatal("reserved group was stolen")
	}
	if got := LocalPorts(listeners); got[0] != original[0] || got[1] != original[1] {
		t.Fatal("original ports changed")
	}
}
