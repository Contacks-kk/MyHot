package media

import "testing"

func TestEgressRoutingKeepsDirectHostsOnGuardedDialer(t *testing.T) {
	router, err := newEgressRouter("http://proxy.example.invalid:8080", []string{"static.example.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"media.example.invalid", "images.example.org"} {
		if !router.shouldProxy(host) {
			t.Errorf("%q should use the configured egress proxy", host)
		}
	}
	for _, host := range []string{"static.example.invalid", "feeds.example.cn", "mirror.local", "203.0.113.11", "[2001:db8::1]"} {
		if router.shouldProxy(host) {
			t.Errorf("%q should use direct guarded dialing", host)
		}
	}
}

func TestEgressRouterRejectsUnsupportedProxyURL(t *testing.T) {
	for _, raw := range []string{"socks5://proxy.example.invalid:1080", "http://proxy.example.invalid/path", "not a url"} {
		if _, err := newEgressRouter(raw, nil); err == nil {
			t.Errorf("proxy URL %q was accepted", raw)
		}
	}
}
