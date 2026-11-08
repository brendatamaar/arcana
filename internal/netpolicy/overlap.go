package netpolicy

import (
	"encoding/hex"
	"errors"
	"net/netip"
	"strings"
)

// HostRoutes parses bounded Linux IPv4 route data, excluding the default route.
func HostRoutes(data string) ([]netip.Prefix, error) {
	if len(data) > 1<<20 {
		return nil, errors.New("route data exceeds cap")
	}
	routes := make([]netip.Prefix, 0)
	for _, line := range strings.Split(data, "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) < 8 || fields[1] == "00000000" {
			continue
		}
		destination, err := hex.DecodeString(fields[1])
		if err != nil || len(destination) != 4 {
			return nil, errors.New("invalid route")
		}
		mask, err := hex.DecodeString(fields[7])
		if err != nil || len(mask) != 4 {
			return nil, errors.New("invalid mask")
		}
		bits := 0
		for i := 3; i >= 0; i-- {
			for shift := 7; shift >= 0; shift-- {
				if mask[i]&(1<<uint(shift)) != 0 {
					bits++
				}
			}
		}
		prefix := netip.PrefixFrom(netip.AddrFrom4([4]byte{destination[3], destination[2], destination[1], destination[0]}), bits).Masked()
		routes = append(routes, prefix)
		if len(routes) > 10000 {
			return nil, errors.New("too many routes")
		}
	}
	return routes, nil
}
