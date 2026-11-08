package netpolicy

import (
	"net/netip"
	"testing"
)

func TestPublic_RejectsReservedIPv4AndIPv6(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "169.254.169.254", "10.0.0.1", "172.16.0.1", "192.168.1.1", "100.64.0.1", "0.0.0.0", "224.0.0.1", "198.18.0.1", "192.0.2.1", "::1", "::", "fc00::1", "fe80::1", "ff02::1", "::ffff:127.0.0.1", "2001:db8::1", "64:ff9b::a00:1", "2002:a00:1::"} {
		if Public(netip.MustParseAddr(ip)) {
			t.Errorf("accepted %s", ip)
		}
	}
	for _, ip := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if !Public(netip.MustParseAddr(ip)) {
			t.Errorf("rejected %s", ip)
		}
	}
}

func TestHost_ExactDNSOnly(t *testing.T) {
	for _, s := range []string{"*.example.com", "127.0.0.1", "foo:80", "https://example.com", "foo/bar", "-foo.com", "a..b", "foo\x00bar"} {
		if _, err := Host(s); err == nil {
			t.Errorf("accepted %q", s)
		}
	}
	if got, err := Host("EXAMPLE.COM."); err != nil || got != "example.com" {
		t.Fatal(got, err)
	}
}

func TestAllocate_OverlapExhaustionAndBounds(t *testing.T) {
	pool := netip.MustParsePrefix("10.200.0.0/24")
	got, err := Allocate(pool, 28, []netip.Prefix{netip.MustParsePrefix("10.200.0.0/28")})
	if err != nil || got.String() != "10.200.0.16/28" {
		t.Fatal(got, err)
	}
	if _, err := Allocate(pool, 28, []netip.Prefix{pool}); err == nil {
		t.Fatal("exhaustion ignored")
	}
	if _, err := Allocate(pool, 23, nil); err == nil {
		t.Fatal("invalid prefix")
	}
}

func TestHostRoutes_ParsesConnectedRoutesAndSkipsDefault(t *testing.T) {
	routes, err := HostRoutes("Iface Destination Gateway Flags RefCnt Use Metric Mask\neth0 00000000 0100000A 0003 0 0 0 00000000\neth0 0000C80A 00000000 0001 0 0 0 00FFFFFF\n")
	if err != nil || len(routes) != 1 || routes[0].String() != "10.200.0.0/24" {
		t.Fatal(routes, err)
	}
}
