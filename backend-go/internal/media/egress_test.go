package media

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type egressTestObservation struct {
	protocol   string
	host       string
	serverName string
}

/** TestOutboundRoutesNegotiateHTTP1AndPreserveTLSIdentity migrates the legacy egress protocol contract. */
func TestOutboundRoutesNegotiateHTTP1AndPreserveTLSIdentity(t *testing.T) {
	for _, route := range []string{"direct", "proxy", "secure-proxy", "dns"} {
		t.Run(route, func(t *testing.T) {
			verifyOutboundRoute(t, route)
		})
	}
}

/** verifyOutboundRoute checks one egress route using only local TLS and proxy fixtures. */
func verifyOutboundRoute(t *testing.T, route string) {
	t.Helper()
	certificate, roots := newEgressTestTLSMaterial(t)
	observations := make(chan egressTestObservation, 8)
	origin := newEgressTestOrigin(t, certificate, observations)
	defer origin.Close()
	proxyTargets := make(chan string, 8)
	proxyProtocols := make(chan string, 8)
	secureProxy := route == "secure-proxy"
	proxy := newEgressTestProxy(t, origin, certificate, secureProxy, proxyTargets, proxyProtocols)
	defer proxy.Close()

	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxyURL.Scheme = "http"
	if secureProxy {
		proxyURL.Scheme = "https"
	}
	directHosts := []string(nil)
	if route == "direct" {
		directHosts = []string{"outbound.invalid"}
	}
	router, err := newEgressRouter(proxyURL.String(), directHosts)
	if err != nil {
		t.Fatal(err)
	}
	dohTransport := router.dohClient.Transport.(*http.Transport)
	dohTransport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	defer dohTransport.CloseIdleConnections()
	if secureProxy {
		router.proxyTLSConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	}

	switch route {
	case "direct":
		router.directDial = func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, origin.Listener.Addr().String())
		}
	case "proxy", "secure-proxy":
		router.lookupDoH = func(context.Context, string) ([]net.IP, time.Duration, error) {
			return []net.IP{net.ParseIP("93.184.216.34")}, time.Minute, nil
		}
	case "dns":
		addresses, _, resolveErr := router.resolve(context.Background(), "outbound.invalid")
		if resolveErr != nil {
			t.Fatal(resolveErr)
		}
		if len(addresses) != 1 || !addresses[0].Equal(net.ParseIP("93.184.216.34")) {
			t.Fatalf("resolved addresses = %v", addresses)
		}
		assertEgressObservations(t, observations, 2, "cloudflare-dns.com")
		if targets := drainStrings(proxyTargets); len(targets) == 0 || !allStringsEqual(targets, "cloudflare-dns.com:443") {
			t.Fatalf("DNS proxy CONNECT targets = %v", targets)
		}
		assertDoHRejectsUntrustedCertificate(t, router)
		return
	default:
		t.Fatalf("unknown outbound route %q", route)
	}

	transport := &http.Transport{
		DialContext: router.dialContext,
		TLSClientConfig: &tls.Config{
			RootCAs:    roots,
			MinVersion: tls.VersionTLS12,
		},
		Protocols: http1OnlyProtocols(),
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	for range 2 {
		request, err := http.NewRequest(http.MethodGet, "https://outbound.invalid/item", nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if readErr != nil || response.StatusCode != http.StatusOK || string(body) != "ok" {
			t.Fatalf("response status/body = %d/%q, err=%v", response.StatusCode, body, readErr)
		}
	}
	assertEgressObservations(t, observations, 2, "outbound.invalid")

	if route == "proxy" || route == "secure-proxy" {
		if targets := drainStrings(proxyTargets); len(targets) == 0 || !allStringsEqual(targets, "93.184.216.34:443") {
			t.Fatalf("proxy CONNECT targets = %v", targets)
		}
	}
	if secureProxy && (len(proxyProtocols) == 0 || <-proxyProtocols != "http/1.1") {
		t.Fatalf("secure proxy ALPN = %v", drainStrings(proxyProtocols))
	}
	assertOutboundRejectsUntrustedCertificate(t, router)
}

/** assertDoHRejectsUntrustedCertificate keeps DNS-over-HTTPS certificate checks enabled. */
func assertDoHRejectsUntrustedCertificate(t *testing.T, router *egressRouter) {
	t.Helper()
	transport := router.dohClient.Transport.(*http.Transport)
	transport.CloseIdleConnections()
	transport.TLSClientConfig = &tls.Config{RootCAs: x509.NewCertPool(), MinVersion: tls.VersionTLS12}
	if _, _, err := router.lookupDoH(context.Background(), "outbound.invalid"); err == nil {
		t.Fatal("DNS-over-HTTPS accepted an untrusted origin certificate")
	}
}

/** assertOutboundRejectsUntrustedCertificate keeps origin certificate checks enabled on media routes. */
func assertOutboundRejectsUntrustedCertificate(t *testing.T, router *egressRouter) {
	t.Helper()
	transport := &http.Transport{
		DialContext: router.dialContext,
		TLSClientConfig: &tls.Config{
			RootCAs:    x509.NewCertPool(),
			MinVersion: tls.VersionTLS12,
		},
		Protocols: http1OnlyProtocols(),
	}
	defer transport.CloseIdleConnections()
	request, err := http.NewRequest(http.MethodGet, "https://outbound.invalid/item", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := (&http.Client{Transport: transport, Timeout: 3 * time.Second}).Do(request)
	if response != nil {
		_ = response.Body.Close()
	}
	if err == nil {
		t.Fatal("outbound route accepted an untrusted origin certificate")
	}
}

/** newEgressTestTLSMaterial creates a local CA certificate valid for origin and proxy identities. */
func newEgressTestTLSMaterial(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "outbound.invalid"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"outbound.invalid", "cloudflare-dns.com"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("93.184.216.34")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	certificate, err := tls.X509KeyPair(certificatePEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	return certificate, roots
}

/** newEgressTestOrigin starts an HTTP/2-capable TLS origin and records negotiated request identity. */
func newEgressTestOrigin(t *testing.T, certificate tls.Certificate, observations chan<- egressTestObservation) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observation := egressTestObservation{protocol: r.Proto, host: r.Host}
		if r.TLS != nil {
			observation.serverName = r.TLS.ServerName
		}
		observations <- observation
		if r.URL.Path == "/dns-query" {
			answer := `{"Status":0,"Answer":[]}`
			if r.URL.Query().Get("type") == "A" {
				answer = `{"Status":0,"Answer":[{"type":1,"data":"93.184.216.34","TTL":60}]}`
			}
			_, _ = io.WriteString(w, answer)
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	server.EnableHTTP2 = true
	server.TLS = &tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"h2", "http/1.1"},
	}
	server.StartTLS()
	return server
}

/** newEgressTestProxy starts a local HTTP or HTTPS CONNECT proxy for an origin fixture. */
func newEgressTestProxy(t *testing.T, origin *httptest.Server, certificate tls.Certificate, secure bool, targets chan<- string, protocols chan<- string) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "expected CONNECT", http.StatusBadRequest)
			return
		}
		targets <- r.RequestURI
		if r.TLS != nil {
			protocols <- r.TLS.NegotiatedProtocol
		}
		conn, buffered, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		upstream, err := net.Dial("tcp", origin.Listener.Addr().String())
		if err != nil {
			_ = conn.Close()
			return
		}
		_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
		if err := buffered.Flush(); err != nil {
			_ = conn.Close()
			_ = upstream.Close()
			return
		}
		go func() {
			_, _ = io.Copy(upstream, buffered.Reader)
			_ = upstream.Close()
		}()
		_, _ = io.Copy(conn, upstream)
		_ = conn.Close()
	}))
	server.EnableHTTP2 = secure
	if secure {
		server.TLS = &tls.Config{
			Certificates: []tls.Certificate{certificate},
			MinVersion:   tls.VersionTLS12,
			NextProtos:   []string{"h2", "http/1.1"},
		}
		server.StartTLS()
	} else {
		server.Start()
	}
	return server
}

/** assertEgressObservations verifies protocol negotiation and TLS host identity for the expected requests. */
func assertEgressObservations(t *testing.T, observations <-chan egressTestObservation, minimum int, host string) {
	t.Helper()
	if len(observations) < minimum {
		t.Fatalf("got %d origin requests, want at least %d", len(observations), minimum)
	}
	for len(observations) > 0 {
		observation := <-observations
		if observation.protocol != "HTTP/1.1" || observation.host != host || observation.serverName != host {
			t.Errorf("unexpected egress request identity: %+v", observation)
		}
	}
}

/** drainStrings formats queued proxy observations for a useful failure message. */
func drainStrings(values <-chan string) []string {
	var result []string
	for len(values) > 0 {
		result = append(result, <-values)
	}
	return result
}

/** allStringsEqual checks that every recorded proxy target preserves the expected authority. */
func allStringsEqual(values []string, expected string) bool {
	for _, value := range values {
		if value != expected {
			return false
		}
	}
	return true
}

/** TestNewServiceRestrictsOutboundProtocolToHTTP1 keeps media fetching on the supported protocol. */
func TestNewServiceRestrictsOutboundProtocolToHTTP1(t *testing.T) {
	service := NewService(Config{}, nil, nil)
	client, ok := service.client.(*http.Client)
	if !ok {
		t.Fatalf("outbound client type = %T", service.client)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.Protocols == nil || !transport.Protocols.HTTP1() || transport.Protocols.HTTP2() {
		t.Fatalf("outbound protocol set = %v", transport.Protocols)
	}
}

func TestEgressRoutingKeepsDirectHostsOnGuardedDialer(t *testing.T) {
	router, err := newEgressRouter("http://proxy.example.invalid:8080", []string{"static.example.invalid", "domestic.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"media.example.invalid", "images.example.org"} {
		if !router.shouldProxy(host) {
			t.Errorf("%q should use the configured egress proxy", host)
		}
	}
	for _, host := range []string{"static.example.invalid", "domestic.invalid", "feeds.example.cn", "mirror.local", "203.0.113.11", "[2001:db8::1]"} {
		if router.shouldProxy(host) {
			t.Errorf("%q should use direct guarded dialing", host)
		}
	}
	for _, host := range []string{"abroad.invalid", "domestic.invalid.evil.invalid"} {
		if !router.shouldProxy(host) {
			t.Errorf("%q should not inherit an exact-host direct route", host)
		}
	}
}

/** TestEgressRedirectReevaluatesDestinationRoute ensures a direct host exception does not follow its redirect. */
func TestEgressRedirectReevaluatesDestinationRoute(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "http://abroad.invalid:8080/final", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, r.Host)
	}))
	defer origin.Close()
	targets := make(chan string, 1)
	proxyProtocols := make(chan string, 1)
	proxy := newEgressTestProxy(t, origin, tls.Certificate{}, false, targets, proxyProtocols)
	defer proxy.Close()
	router, err := newEgressRouter(proxy.URL, []string{"domestic.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	router.directDial = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, origin.Listener.Addr().String())
	}
	router.lookupDoH = func(context.Context, string) ([]net.IP, time.Duration, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, time.Minute, nil
	}
	transport := &http.Transport{Proxy: nil, DialContext: router.dialContext}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	response, err := client.Get("http://domestic.invalid:8080/start")
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK || string(body) != "abroad.invalid:8080" {
		t.Fatalf("redirect response status/body = %d/%q, err=%v", response.StatusCode, body, readErr)
	}
	if target := <-targets; target != "93.184.216.34:8080" {
		t.Fatalf("redirect CONNECT target = %q, want public pinned address", target)
	}
}

func TestEgressRouterRejectsUnsupportedProxyURL(t *testing.T) {
	for _, raw := range []string{"socks5://proxy.example.invalid:1080", "http://proxy.example.invalid/path", "not a url"} {
		if _, err := newEgressRouter(raw, nil); err == nil {
			t.Errorf("proxy URL %q was accepted", raw)
		}
	}
}

/** TestEgressRouterPinsPublicDNSAndPreservesOriginalHost verifies CONNECT targets and origin headers. */
func TestEgressRouterPinsPublicDNSAndPreservesOriginalHost(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Host)
	}))
	defer origin.Close()
	targets := make(chan string, 2)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "expected CONNECT", http.StatusBadRequest)
			return
		}
		conn, buffered, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		targets <- r.RequestURI
		upstream, err := net.Dial("tcp", origin.Listener.Addr().String())
		if err != nil {
			_ = conn.Close()
			return
		}
		_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
		if err := buffered.Flush(); err != nil {
			_ = conn.Close()
			_ = upstream.Close()
			return
		}
		go func() {
			_, _ = io.Copy(upstream, buffered.Reader)
			_ = upstream.Close()
		}()
		_, _ = io.Copy(conn, upstream)
		_ = conn.Close()
	}))
	defer proxy.Close()

	for _, test := range []struct {
		name      string
		ip        string
		authority string
	}{
		{name: "IPv4", ip: "93.184.216.34", authority: "93.184.216.34:8080"},
		{name: "IPv6", ip: "2606:4700:4700::1111", authority: "[2606:4700:4700::1111]:8080"},
	} {
		t.Run(test.name, func(t *testing.T) {
			router, err := newEgressRouter(proxy.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			router.lookupDoH = func(context.Context, string) ([]net.IP, time.Duration, error) {
				return []net.IP{net.ParseIP(test.ip)}, time.Minute, nil
			}
			transport := &http.Transport{Proxy: nil, DialContext: router.dialContext}
			defer transport.CloseIdleConnections()
			response, err := (&http.Client{Transport: transport, Timeout: 3 * time.Second}).Get("http://image.invalid:8080/photo")
			if err != nil {
				t.Fatal(err)
			}
			body, readErr := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if readErr != nil || string(body) != "image.invalid:8080" {
				t.Fatalf("response host = %q, err=%v", body, readErr)
			}
			if target := <-targets; target != test.authority {
				t.Fatalf("CONNECT authority = %q, want %q", target, test.authority)
			}
		})
	}
}

/** TestEgressRouterRejectsPrivateResolverAnswersBeforeConnecting verifies invalid DNS never reaches the proxy. */
func TestEgressRouterRejectsPrivateResolverAnswersBeforeConnecting(t *testing.T) {
	t.Setenv("ALLOW_FAKE_IP", "")
	var proxyHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyHits.Add(1)
		http.Error(w, "unexpected proxy request", http.StatusBadGateway)
	}))
	defer proxy.Close()
	for _, addresses := range [][]net.IP{
		{net.ParseIP("127.0.0.1")},
		{net.ParseIP("93.184.216.34"), net.ParseIP("10.0.0.1")},
		{net.ParseIP("::ffff:169.254.169.254")},
		{net.ParseIP("2001:db8::1")},
		{net.ParseIP("198.18.0.1")},
		{net.ParseIP("198.19.255.255")},
		{net.ParseIP("not-an-ip")},
		nil,
	} {
		router, err := newEgressRouter(proxy.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		router.lookupDoH = func(context.Context, string) ([]net.IP, time.Duration, error) {
			return addresses, time.Minute, nil
		}
		if _, err := router.dialContext(context.Background(), "tcp", "image.invalid:80"); err == nil {
			t.Fatalf("resolver answer %v was accepted", addresses)
		}
	}
	if got := proxyHits.Load(); got != 0 {
		t.Fatalf("invalid DNS answers reached the proxy %d times", got)
	}
}

/** TestEgressDirectHostCannotBypassPrivateAddressGuard ensures host exceptions still use guarded dialing. */
func TestEgressDirectHostCannotBypassPrivateAddressGuard(t *testing.T) {
	router, err := newEgressRouter("http://proxy.example.invalid:8080", []string{"localhost"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := router.dialContext(context.Background(), "tcp", "localhost:80"); err == nil || strings.Contains(err.Error(), "proxy") {
		t.Fatalf("direct host bypassed the private-address guard: %v", err)
	}
}
