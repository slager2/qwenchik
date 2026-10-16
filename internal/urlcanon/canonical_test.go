package urlcanon

import "testing"

func TestCanonicalNormalizesTrackingAndHost(t *testing.T) {
	canonical, err := Canonical("HTTPS://EXAMPLE.COM:443/path?utm_source=news&b=2&a=1#section")
	if err != nil {
		t.Fatal(err)
	}
	if canonical != "https://example.com/path?a=1&b=2" {
		t.Fatalf("canonical URL = %q", canonical)
	}
}

func TestCanonicalRejectsUnsafeURLs(t *testing.T) {
	for _, raw := range []string{"file:///etc/passwd", "https://user:pass@example.com/", "//example.com/", "https://"} {
		if _, err := Canonical(raw); err == nil {
			t.Errorf("Canonical(%q) succeeded, want error", raw)
		}
	}
}
