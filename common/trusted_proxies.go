package common

import (
	"bytes"
	"errors"
	"fmt"
	"net"
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
// way gin applies it, trusts every IPv4 or every IPv6 address: a range with
// prefix length 0 however it is written (0.0.0.0/0, ::/0, 10.1.2.3/0), or
// ::ffff:0:0/96, which gin applies to every IPv4 client. Every client can then
// set the address the server sees for it with X-Forwarded-For.
func TrustedProxiesCatchAll(trustedProxies []string) bool {
	lastIPv6 := net.IP(bytes.Repeat([]byte{0xff}, net.IPv6len))
	for _, trustedProxy := range trustedProxies {
		// gin parses a range with net.ParseCIDR; an entry without a prefix
		// length is one address.
		_, network, err := net.ParseCIDR(trustedProxy)
		if err != nil {
			continue
		}
		// gin trusts a peer that the range contains, and a range that contains
		// the first and the last address of a family contains all of it.
		if network.Contains(net.IPv4zero) && network.Contains(net.IPv4bcast) ||
			network.Contains(net.IPv6zero) && network.Contains(lastIPv6) {
			return true
		}
	}
	return false
}
