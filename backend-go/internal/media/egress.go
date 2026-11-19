package media

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"aihot/backend-go/internal/modelconfig"
)

type egressRouter struct {
	proxy          *url.URL
	direct         map[string]struct{}
	dohClient      *http.Client
	directDial     func(context.Context, string, string) (net.Conn, error)
	proxyTLSConfig *tls.Config
	lookupDoH      func(context.Context, string) ([]net.IP, time.Duration, error)
	mu             sync.Mutex
	cache          map[string]dnsEntry
}

type dnsEntry struct {
	addresses []net.IP
	until     time.Time
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

/** NewEgressDialer applies the configured guarded proxy route to public data fetches. */
func NewEgressDialer(rawProxy string, directHosts []string) (func(context.Context, string, string) (net.Conn, error), error) {
	if strings.TrimSpace(rawProxy) == "" {
		return modelconfig.DialPublic, nil
	}
	router, err := newEgressRouter(rawProxy, directHosts)
	if err != nil {
		return nil, err
	}
	return router.dialContext, nil
}

/** http1OnlyProtocols limits outbound requests to HTTP/1.1 for compatible egress routes. */
func http1OnlyProtocols() *http.Protocols {
	protocols := &http.Protocols{}
	protocols.SetHTTP1(true)
	return protocols
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

func newEgressRouter(rawProxy string, directHosts []string) (*egressRouter, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawProxy))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("EGRESS_PROXY_URL must be an HTTP or HTTPS proxy URL")
	}
	transport := &http.Transport{
		Proxy:                  http.ProxyURL(parsed),
		TLSHandshakeTimeout:    5 * time.Second,
		ResponseHeaderTimeout:  4 * time.Second,
		MaxResponseHeaderBytes: 64 * 1024,
		Protocols:              http1OnlyProtocols(),
	}
	router := &egressRouter{
		proxy:      parsed,
		direct:     map[string]struct{}{},
		dohClient:  &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		directDial: modelconfig.DialPublic,
		cache:      map[string]dnsEntry{},
	}
	for _, host := range directHosts {
		host = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(host, ".")))
		if host != "" {
			router.direct[host] = struct{}{}
		}
	}
	// Resolve names through the configured egress route and pin each request to a checked answer.
	// The proxy never gets to resolve the original untrusted hostname itself.
	router.lookupDoH = func(ctx context.Context, host string) ([]net.IP, time.Duration, error) {
		var found []net.IP
		var ttl time.Duration
		for _, recordType := range []string{"A", "AAAA"} {
			query := url.Values{"name": {host}, "type": {recordType}}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://cloudflare-dns.com/dns-query?"+query.Encode(), nil)
			if err != nil {
				return nil, 0, err
			}
			req.Header.Set("Accept", "application/dns-json")
			resp, err := router.dohClient.Do(req)
			if err != nil {
				return nil, 0, err
			}
			var result struct {
				Status  int `json:"Status"`
				Answers []struct {
					Type int    `json:"type"`
					Data string `json:"data"`
					TTL  int    `json:"TTL"`
				} `json:"Answer"`
			}
			decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&result)
			_ = resp.Body.Close()
			if decodeErr != nil || resp.StatusCode != http.StatusOK || result.Status != 0 {
				return nil, 0, fmt.Errorf("egress DNS lookup failed for %s", host)
			}
			for _, answer := range result.Answers {
				if answer.Type != 1 && answer.Type != 28 {
					continue
				}
				ip := net.ParseIP(answer.Data)
				if ip == nil {
					continue
				}
				found = append(found, ip)
				seconds := answer.TTL
				if seconds < 0 {
					seconds = 0
				}
				if ttl == 0 || time.Duration(seconds)*time.Second < ttl {
					ttl = time.Duration(seconds) * time.Second
				}
			}
		}
		if len(found) == 0 {
			return nil, 0, fmt.Errorf("egress DNS lookup returned no addresses for %s", host)
		}
		for _, ip := range found {
			if !modelconfig.IsPublicIP(ip) {
				return nil, 0, fmt.Errorf("egress DNS returned a non-public address for %s", host)
			}
		}
		if ttl > time.Minute {
			ttl = time.Minute
		}
		return found, ttl, nil
	}
	return router, nil
}

func (r *egressRouter) resolve(ctx context.Context, host string) ([]net.IP, time.Duration, error) {
	if addr, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		ip := net.IP(addr.AsSlice())
		if !modelconfig.IsPublicIP(ip) {
			return nil, 0, errors.New("image host resolved to a non-public address")
		}
		return []net.IP{ip}, time.Minute, nil
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	r.mu.Lock()
	if entry, ok := r.cache[host]; ok && time.Now().Before(entry.until) {
		r.mu.Unlock()
		return entry.addresses, time.Until(entry.until), nil
	}
	r.mu.Unlock()
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addresses, ttl, err := r.lookupDoH(lookupCtx, host)
	if err != nil {
		return nil, 0, err
	}
	if len(addresses) == 0 {
		return nil, 0, errors.New("egress DNS lookup returned no addresses")
	}
	for _, address := range addresses {
		if !modelconfig.IsPublicIP(address) {
			return nil, 0, errors.New("egress DNS returned a non-public address")
		}
	}
	r.mu.Lock()
	if len(r.cache) >= 256 {
		for name := range r.cache {
			delete(r.cache, name)
			break
		}
	}
	r.cache[host] = dnsEntry{addresses: addresses, until: time.Now().Add(ttl)}
	r.mu.Unlock()
	return addresses, ttl, nil
}

func (r *egressRouter) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	if !r.shouldProxy(host) {
		return r.directDial(ctx, network, address)
	}
	addresses, _, err := r.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	var failures []error
	for _, ip := range addresses {
		conn, dialErr := r.dialTunnel(ctx, network, net.JoinHostPort(ip.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		failures = append(failures, dialErr)
	}
	return nil, fmt.Errorf("image proxy destination is unavailable: %w", errors.Join(failures...))
}

func (r *egressRouter) shouldProxy(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
	_, directHost := r.direct[host]
	return r.proxy != nil && net.ParseIP(host) == nil && !strings.HasSuffix(host, ".cn") && !strings.HasSuffix(host, ".local") && !directHost
}

func (r *egressRouter) dialTunnel(ctx context.Context, network, target string) (net.Conn, error) {
	proxyPort := r.proxy.Port()
	if proxyPort == "" {
		if r.proxy.Scheme == "https" {
			proxyPort = "443"
		} else {
			proxyPort = "80"
		}
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(r.proxy.Hostname(), proxyPort))
	if err != nil {
		return nil, err
	}
	if r.proxy.Scheme == "https" {
		proxyTLS := &tls.Config{ServerName: r.proxy.Hostname(), MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}}
		if r.proxyTLSConfig != nil {
			proxyTLS = r.proxyTLSConfig.Clone()
			proxyTLS.ServerName = r.proxy.Hostname()
			proxyTLS.MinVersion = tls.VersionTLS12
			proxyTLS.NextProtos = []string{"http/1.1"}
		}
		tlsConn := tls.Client(conn, proxyTLS)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, err
		}
		conn = tlsConn
	}
	authority := target
	request := "CONNECT " + authority + " HTTP/1.1\r\nHost: " + authority + "\r\n"
	if r.proxy.User != nil {
		password, _ := r.proxy.User.Password()
		credentials := base64.StdEncoding.EncodeToString([]byte(r.proxy.User.Username() + ":" + password))
		request += "Proxy-Authorization: Basic " + credentials + "\r\n"
	}
	request += "Proxy-Connection: Keep-Alive\r\n\r\n"
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(conn, request); err != nil {
		_ = conn.Close()
		return nil, err
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		_ = conn.Close()
		return nil, fmt.Errorf("egress proxy CONNECT returned %d", response.StatusCode)
	}
	_ = conn.SetDeadline(time.Time{})
	return &bufferedConn{Conn: conn, reader: reader}, nil
}
