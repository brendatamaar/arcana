// Package netpolicy enforces bounded hostname policies and public-IP-only egress.
package netpolicy

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"strings"
)

// Host canonicalizes an exact DNS hostname; wildcards, IP literals and URL syntax are rejected.
func Host(value string) (string, error) {
	value = strings.ToLower(strings.TrimSuffix(value, "."))
	if len(value) == 0 || len(value) > 253 {
		return "", errors.New("invalid hostname")
	}
	if _, err := netip.ParseAddr(value); err == nil {
		return "", errors.New("IP literals are not hostnames")
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("invalid DNS label")
		}
		for _, c := range label {
			if c != '-' && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
				return "", errors.New("invalid hostname character")
			}
		}
	}
	return value, nil
}

// Public rejects special-purpose ranges, including IPv4-mapped IPv6.
func Public(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, raw := range []string{"0.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/3", "::/96", "::ffff:0:0/96", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/23", "2001:db8::/32", "2002::/16", "fc00::/7", "fe80::/10", "ff00::/8"} {
		if netip.MustParsePrefix(raw).Contains(ip) {
			return false
		}
	}
	return ip.Is4() || netip.MustParsePrefix("2000::/3").Contains(ip)
}

// Allocate returns the first unoccupied subnet. Iteration is capped to 65536 candidates.
func Allocate(pool netip.Prefix, bits int, used []netip.Prefix) (netip.Prefix, error) {
	if !pool.IsValid() || !pool.Addr().Is4() || pool != pool.Masked() || bits < pool.Bits() || bits > 30 || bits-pool.Bits() > 16 || len(used) > 10000 {
		return netip.Prefix{}, errors.New("invalid or oversized subnet pool")
	}
	address := pool.Addr()
	for range 1 << uint(bits-pool.Bits()) {
		candidate := netip.PrefixFrom(address, bits)
		free := true
		for _, existing := range used {
			if candidate.Overlaps(existing) {
				free = false
				break
			}
		}
		if free {
			return candidate, nil
		}
		bytes := address.As4()
		n := uint32(bytes[0])<<24 | uint32(bytes[1])<<16 | uint32(bytes[2])<<8 | uint32(bytes[3])
		n += 1 << uint(32-bits)
		var next [4]byte
		binary.BigEndian.PutUint32(next[:], n)
		address = netip.AddrFrom4(next)
	}
	return netip.Prefix{}, errors.New("subnet pool exhausted")
}
