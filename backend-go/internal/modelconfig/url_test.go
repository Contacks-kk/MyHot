package modelconfig

import (
	"context"
	"net"
	"testing"
)

func TestIsPublicIP(t *testing.T) {
	for _, raw := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		if !IsPublicIP(net.ParseIP(raw)) {
			t.Errorf("%s should be public", raw)
		}
	}
	for _, raw := range []string{"127.0.0.1", "10.1.2.3", "169.254.169.254", "100.64.0.1", "192.0.2.10", "::1", "fc00::1", "fe80::1", "2001:db8::1", "::ffff:192.168.1.1"} {
		if IsPublicIP(net.ParseIP(raw)) {
			t.Errorf("%s should not be public", raw)
		}
	}
	t.Setenv("ALLOW_FAKE_IP", "")
	if IsPublicIP(net.ParseIP("198.18.0.1")) {
		t.Errorf("198.18.0.1 should not be public by default")
	}
	t.Setenv("ALLOW_FAKE_IP", "true")
	if !IsPublicIP(net.ParseIP("198.18.0.1")) {
		t.Errorf("198.18.0.1 should be public when ALLOW_FAKE_IP=true")
	}
}

func TestValidateBaseURL(t *testing.T) {
	ctx := context.Background()
	for _, raw := range []string{"http://8.8.8.8/v1", "https://user:pass@example.com/v1", "https://8.8.8.8/v1?key=secret", "https://127.0.0.1/v1", "https://[::1]/v1", "https://service.local/v1"} {
		if _, err := ValidateBaseURL(ctx, raw); err == nil {
			t.Errorf("%q should be rejected", raw)
		}
	}
	if _, err := ValidateBaseURL(ctx, "https://8.8.8.8/v1"); err != nil {
		t.Fatalf("public HTTPS endpoint rejected: %v", err)
	}
}
