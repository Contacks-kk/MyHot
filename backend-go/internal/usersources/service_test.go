package usersources

import (
	"testing"
)

func TestValidateRSSSubscriptionBoundsAndURLRules(t *testing.T) {
	valid := Input{Name: "News", URL: "https://example.com/feed.xml", IntervalMinutes: 60, ReuseWindowMinutes: 10}
	if err := validate(valid); err != nil {
		t.Fatalf("valid subscription rejected: %v", err)
	}
	for _, mutate := range []func(*Input){
		func(v *Input) { v.Name = " " },
		func(v *Input) { v.URL = "file:///etc/passwd" },
		func(v *Input) { v.URL = "https://user:pass@example.com/feed" },
		func(v *Input) { v.URL = "https://example.com/feed#fragment" },
		func(v *Input) { v.IntervalMinutes = 0 },
		func(v *Input) { v.IntervalMinutes = 1441 },
		func(v *Input) { v.ReuseWindowMinutes = 0 },
		func(v *Input) { v.ReuseWindowMinutes = 1441 },
	} {
		input := valid
		mutate(&input)
		if err := validate(input); err == nil {
			t.Errorf("invalid input accepted: %#v", input)
		}
	}
}

func TestIdentityCanonicalizesHostAndQueryOrderWithoutDroppingValues(t *testing.T) {
	if identityFor("HTTP://Example.com:80/feed?b=2&a=1") != identityFor("http://example.com/feed?a=1&b=2") {
		t.Fatal("equivalent request URLs should share an identity")
	}
	if identityFor("https://example.com/feed?token=one") == identityFor("https://example.com/feed?token=two") {
		t.Fatal("query values that can affect the response must remain distinct")
	}
}

func TestCreateDefaultsToOneHourAndTenMinuteReuseWindow(t *testing.T) {
	got := withDefaults(Input{Name: "News", URL: "https://example.com/feed.xml"})
	if got.IntervalMinutes != 60 || got.ReuseWindowMinutes != 10 {
		t.Fatalf("subscription defaults = %d/%d, want 60/10", got.IntervalMinutes, got.ReuseWindowMinutes)
	}
}
