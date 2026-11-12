package modelconfig

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"
)

// ErrNonPublicAddress marks a DNS answer rejected before any connection is made.
var ErrNonPublicAddress = errors.New("model host resolved to a non-public address")

var fakeIPPrefix = netip.MustParsePrefix("198.18.0.0/15")

/** 检查是否允许使用 Fake-IP */
func isFakeIPAllowed() bool {
	return os.Getenv("ALLOW_FAKE_IP") == "true"
}

var restrictedPrefixes = func() []netip.Prefix {
	values := []string{
		"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24",
		"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
		"64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/23", "2001:db8::/32", "2002::/16",
	}
	prefixes := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		prefixes = append(prefixes, netip.MustParsePrefix(value))
	}
	return prefixes
}()

/** 判断 IP 是否为公网地址 */
func IsPublicIP(ip net.IP) bool {
	addr, err := netip.ParseAddr(ip.String())
	if err != nil || addr.Is4In6() || !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsUnspecified() || addr.IsMulticast() {
		return false
	}
	if isFakeIPAllowed() && fakeIPPrefix.Contains(addr) {
		return true
	}
	for _, prefix := range restrictedPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

func ValidateBaseURL(ctx context.Context, raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("baseUrl 必须是无用户名、查询参数和片段的公网 HTTPS 地址")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return nil, errors.New("baseUrl 必须指向公网主机")
	}
	if ip := net.ParseIP(host); ip != nil {
		if !IsPublicIP(ip) {
			return nil, errors.New("baseUrl 必须指向公网主机")
		}
		return u, nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(lookupCtx, host)
	if err != nil || len(ips) == 0 {
		return nil, errors.New("baseUrl 主机名无法解析到公网地址")
	}
	for _, addr := range ips {
		if !IsPublicIP(addr.IP) {
			return nil, errors.New("baseUrl 主机名包含非公网地址")
		}
	}
	return u, nil
}

func DialPublic(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(lookupCtx, host)
	if err != nil || len(ips) == 0 {
		return nil, errors.New("model host did not resolve")
	}
	// Validate the entire answer before connecting, including after a DNS change.
	for _, addr := range ips {
		if !IsPublicIP(addr.IP) {
			return nil, ErrNonPublicAddress
		}
	}
	var failures []error
	for _, addr := range ips {
		ip := addr.IP
		conn, dialErr := (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		failures = append(failures, dialErr)
	}
	return nil, fmt.Errorf("model host is unavailable: %w", errors.Join(failures...))
}
