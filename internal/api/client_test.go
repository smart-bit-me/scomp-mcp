package api

import "testing"

func TestParseServerURL(t *testing.T) {
	for _, value := range []string{"https://link.scomp.me", "https://example.com:8443", "http://localhost:8080", "http://127.0.0.1:8080"} {
		if _, err := ParseServerURL(value); err != nil {
			t.Fatalf("ParseServerURL(%q): %v", value, err)
		}
	}
	for _, value := range []string{"http://example.com", "https://user@example.com", "https://example.com/path", "https://example.com?q=x", "file:///tmp/x"} {
		if _, err := ParseServerURL(value); err == nil {
			t.Fatalf("ParseServerURL(%q) unexpectedly succeeded", value)
		}
	}
}
