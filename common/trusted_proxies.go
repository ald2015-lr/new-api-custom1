package common

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"
)

var defaultTrustedProxyCIDRs = []string{
	"127.0.0.0/8",
	"::1",
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"fc00::/7",
}

// ResolveTrustedProxies parses TRUSTED_PROXIES without applying it to an
// engine. The returned slice can be reused by the outer and plugin engines.
func ResolveTrustedProxies(raw string) (trustedProxies []string, usedDefaults bool, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return append([]string(nil), defaultTrustedProxyCIDRs...), true, nil
	}
	if strings.EqualFold(raw, "none") {
		return nil, false, nil
	}

	parts := strings.Split(raw, ",")
	trustedProxies = make([]string, 0, len(parts))
	for _, part := range parts {
		trustedProxy := strings.TrimSpace(part)
		if trustedProxy == "" {
			continue
		}
		if strings.EqualFold(trustedProxy, "none") {
			return nil, false, errors.New("TRUSTED_PROXIES=none must be used alone")
		}
		trustedProxies = append(trustedProxies, trustedProxy)
	}
	if len(trustedProxies) == 0 {
		return nil, false, errors.New("TRUSTED_PROXIES does not contain an IP address or CIDR")
	}
	return trustedProxies, false, nil
}

func ConfigureTrustedProxies(engine *gin.Engine, trustedProxies []string) error {
	if err := engine.SetTrustedProxies(trustedProxies); err != nil {
		return fmt.Errorf("invalid TRUSTED_PROXIES: %w", err)
	}
	return nil
}

// TrustedProxiesCatchAll reports whether a trusted proxy list, applied the
// way gin applies it, trusts every IPv4 or every IPv6 address, through one
// range or through several that together cover the family: a range with
// prefix length 0 however it is written (0.0.0.0/0, ::/0, 10.1.2.3/0), the
// halves 0.0.0.0/1 and 128.0.0.0/1 or ::/1 and 8000::/1, or ::ffff:0:0/96,
// which gin applies to every IPv4 client. Every client can then set the
// address the server sees for it with X-Forwarded-For.
func TrustedProxiesCatchAll(trustedProxies []string) bool {
	type addressRange struct{ first, last netip.Addr }
	// gin compares an IPv4 client with the ranges whose address is IPv4 or
	// IPv4-mapped IPv6, through the last 32 bits of their mask, and an IPv6
	// client with the other ranges.
	families := [2]struct {
		first  netip.Addr
		ranges []addressRange
	}{{first: netip.IPv4Unspecified()}, {first: netip.IPv6Unspecified()}}
	for _, trustedProxy := range trustedProxies {
		// Like gin, an entry without a prefix length is one address.
		if !strings.Contains(trustedProxy, "/") {
			if net.ParseIP(trustedProxy).To4() != nil {
				trustedProxy += "/32"
			} else {
				trustedProxy += "/128"
			}
		}
		_, network, err := net.ParseCIDR(trustedProxy)
		if err != nil {
			continue
		}
		family, ip := &families[0], network.IP.To4()
		if ip == nil {
			family, ip = &families[1], network.IP
		}
		mask := network.Mask[len(network.Mask)-len(ip):]
		last := make(net.IP, len(ip))
		for i := range ip {
			last[i] = ip[i] | ^mask[i]
		}
		first, _ := netip.AddrFromSlice(ip)
		end, _ := netip.AddrFromSlice(last)
		family.ranges = append(family.ranges, addressRange{first, end})
	}
	for _, family := range families {
		// Taken in order of their first address, the ranges cover the family
		// when none starts past the addresses the ones before it cover, until
		// one reaches the family's last address, which has no next address.
		slices.SortFunc(family.ranges, func(a, b addressRange) int { return a.first.Compare(b.first) })
		uncovered := family.first
		for _, r := range family.ranges {
			if uncovered.Less(r.first) {
				break
			}
			if r.last.Less(uncovered) {
				continue
			}
			if uncovered = r.last.Next(); !uncovered.IsValid() {
				return true
			}
		}
	}
	return false
}
