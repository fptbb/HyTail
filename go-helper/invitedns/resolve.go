// SPDX-License-Identifier: MIT
// Package invitedns resolves an HyTail capability via an SRV target's TXT.
// Note that publishing a Tailcat address makes its capability publicly readable.
package invitedns

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

const MaxCodeBytes = 50000
const prefix = "hytailscale="

type resolver interface {
	LookupSRV(context.Context, string, string, string) (string, []*net.SRV, error)
	LookupTXT(context.Context, string) ([]string, error)
}

func IsInvite(s string) bool {
	return strings.HasPrefix(s, "HTBR1:") || strings.HasPrefix(s, "TCBR1:")
}

func validDomain(s string) bool {
	if len(s) < 3 || len(s) > 253 || strings.ContainsAny(s, ":/\\@ \t\r\n") {
		return false
	}
	labels := strings.Split(strings.TrimSuffix(s, "."), ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if len(l) == 0 || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for _, b := range []byte(l) {
			if !((b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '-') {
				return false
			}
		}
	}
	return true
}

// Resolve accepts bridge codes directly or an ordinary DNS name.
// DNS convention: _hytailscale._tcp.DOMAIN SRV 0 0 0 INVITEHOST
// and INVITEHOST TXT "hytailscale=TCBR1:..." (or HTBR1:...).
// The SRV port is intentionally ignored: remote and local ports are separate.
func Resolve(ctx context.Context, codeOrDomain string) (string, error) {
	return resolveWith(ctx, net.DefaultResolver, codeOrDomain)
}

func resolveWith(ctx context.Context, r resolver, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if IsInvite(raw) {
		if len(raw) > MaxCodeBytes {
			return "", errors.New("invite is too large")
		}
		return raw, nil
	}
	if !validDomain(raw) {
		return "", errors.New("expected an HTBR1:/TCBR1: invite or a valid DNS domain")
	}
	deadline, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	_, records, err := r.LookupSRV(deadline, "hytailscale", "tcp", strings.TrimSuffix(raw, "."))
	if err != nil {
		return "", fmt.Errorf("_hytailscale._tcp SRV lookup failed: %w", err)
	}
	if len(records) != 1 || records[0].Target == "." {
		return "", errors.New("expected exactly one _hytailscale._tcp SRV target")
	}
	target := strings.TrimSuffix(records[0].Target, ".")
	if !validDomain(target) {
		return "", errors.New("invalid invite TXT target in SRV record")
	}
	txts, err := r.LookupTXT(deadline, target)
	if err != nil {
		return "", fmt.Errorf("invite TXT lookup failed for %s: %w", target, err)
	}
	var found string
	for _, txt := range txts {
		if !strings.HasPrefix(txt, prefix) {
			continue
		}
		code := strings.TrimPrefix(txt, prefix)
		if !IsInvite(code) || len(code) > MaxCodeBytes {
			return "", errors.New("invalid hytailscale TXT payload")
		}
		if found != "" {
			return "", errors.New("multiple hytailscale TXT invites found")
		}
		found = code
	}
	if found == "" {
		return "", errors.New("SRV target has no hytailscale= TXT invite")
	}
	return found, nil
}
