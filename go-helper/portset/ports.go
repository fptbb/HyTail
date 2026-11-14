// SPDX-License-Identifier: MIT
// Package portset handles bounded port lists and explicit local port mappings.
package portset

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const MaxPorts = 32

func Parse(text string) ([]int, error) {
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("port specification is empty")
	}
	out := []int{}
	for _, item := range strings.Split(text, ",") {
		part := strings.TrimSpace(item)
		if part == "" {
			return nil, fmt.Errorf("empty port list item")
		}
		bits := strings.Split(part, "-")
		if len(bits) > 2 {
			return nil, fmt.Errorf("invalid range %q", part)
		}
		parseOne := func(v string) (int, error) {
			v = strings.TrimSpace(v)
			if v == "" {
				return 0, fmt.Errorf("empty port")
			}
			for _, c := range v {
				if c < '0' || c > '9' {
					return 0, fmt.Errorf("invalid port %q", v)
				}
			}
			n, e := strconv.Atoi(v)
			if e != nil || n < 1 || n > 65535 {
				return 0, fmt.Errorf("port out of range: %q", v)
			}
			return n, nil
		}
		a, e := parseOne(bits[0])
		if e != nil {
			return nil, e
		}
		b := a
		if len(bits) == 2 {
			b, e = parseOne(bits[1])
			if e != nil {
				return nil, e
			}
		}
		if a > b {
			return nil, fmt.Errorf("descending range %q", part)
		}
		if len(out)+b-a+1 > MaxPorts {
			return nil, fmt.Errorf("maximum %d ports per bridge", MaxPorts)
		}
		for n := a; n <= b; n++ {
			out = append(out, n)
		}
	}
	sort.Ints(out)
	for i := 1; i < len(out); i++ {
		if out[i-1] == out[i] {
			return nil, fmt.Errorf("duplicate port %d", out[i])
		}
	}
	return out, nil
}

func Mapping(remote []int, request []int) ([]int, error) {
	if len(remote) == 0 || len(remote) > MaxPorts {
		return nil, fmt.Errorf("invalid remote port count")
	}
	if len(request) == 0 {
		return append([]int(nil), remote...), nil
	}
	local := append([]int(nil), request...)
	if len(local) == 1 && len(remote) > 1 {
		base := local[0] - remote[0]
		local = make([]int, len(remote))
		for i, p := range remote {
			local[i] = p + base
		}
	} else if len(local) != len(remote) {
		return nil, fmt.Errorf("local port list must contain one base port or exactly %d ports", len(remote))
	}
	seen := map[int]bool{}
	for _, p := range local {
		if p < 1 || p > 65535 {
			return nil, fmt.Errorf("local port outside 1-65535")
		}
		if seen[p] {
			return nil, fmt.Errorf("duplicate local port %d", p)
		}
		seen[p] = true
	}
	return local, nil
}

func CSV(ports []int) string {
	out := make([]string, len(ports))
	for i, p := range ports {
		out[i] = strconv.Itoa(p)
	}
	return strings.Join(out, ",")
}
