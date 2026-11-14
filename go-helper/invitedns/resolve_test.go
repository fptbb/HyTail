package invitedns

import (
	"context"
	"errors"
	"net"
	"testing"
)

type fake struct {
	targets []*net.SRV
	txt     []string
	err     error
}

func (f fake) LookupSRV(_ context.Context, a, b, c string) (string, []*net.SRV, error) {
	if a != "hytailscale" || b != "tcp" || c != "test.example" {
		return "", nil, errors.New("bad query")
	}
	return "", f.targets, f.err
}
func (f fake) LookupTXT(_ context.Context, name string) ([]string, error) {
	if name != "invite.example" {
		return nil, errors.New("bad target")
	}
	return f.txt, nil
}
func TestResolve(t *testing.T) {
	f := fake{targets: []*net.SRV{{Target: "invite.example.", Port: 0}}, txt: []string{"misc=hello", "hytailscale=TCBR1:sample"}}
	s, e := resolveWith(context.Background(), f, "test.example")
	if e != nil || s != "TCBR1:sample" {
		t.Fatalf("%s %v", s, e)
	}
	if _, e = resolveWith(context.Background(), f, "https://test.example"); e == nil {
		t.Fatal("accepted URL")
	}
	if _, e = resolveWith(context.Background(), fake{targets: f.targets, txt: []string{"x"}}, "test.example"); e == nil {
		t.Fatal("accepted missing TXT")
	}
	if _, e = resolveWith(context.Background(), fake{targets: append(f.targets, f.targets...), txt: f.txt}, "test.example"); e == nil {
		t.Fatal("ambiguous SRV accepted")
	}
}
