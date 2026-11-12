package config

import "testing"

func TestImageProxyBudgetsUseDecimalMBAndGB(t *testing.T) {
	t.Setenv("IMGPROXY_UPSTREAM_MB_PER_MINUTE", "1.5")
	if got := envByteBudget("IMGPROXY_UPSTREAM_MB_PER_MINUTE", 1_000_000); got != 1_500_000 {
		t.Fatalf("minute budget = %d, want 1500000", got)
	}
	t.Setenv("IMGPROXY_UPSTREAM_GB_PER_DAY", "2")
	if got := envByteBudget("IMGPROXY_UPSTREAM_GB_PER_DAY", 1_000_000_000); got != 2_000_000_000 {
		t.Fatalf("daily budget = %d, want 2000000000", got)
	}
	for _, raw := range []string{"", "invalid", "-1", "NaN", "Inf"} {
		t.Setenv("IMGPROXY_UPSTREAM_MB_PER_MINUTE", raw)
		if got := envByteBudget("IMGPROXY_UPSTREAM_MB_PER_MINUTE", 1_000_000); got != 0 {
			t.Errorf("budget %q = %d, want unlimited sentinel 0", raw, got)
		}
	}
}
