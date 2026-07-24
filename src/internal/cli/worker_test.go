package cli

import "testing"

func TestQueueControlPlaneURL(t *testing.T) {
	// Plain HTTP (tlsEnabled=false)
	for input, want := range map[string]string{
		":8080":                  "http://127.0.0.1:8080",
		"0.0.0.0:8080":          "http://127.0.0.1:8080",
		"apiary.local:8080":     "http://apiary.local:8080",
		"https://apiary.example": "https://apiary.example",
	} {
		if got := queueControlPlaneURL(input, false); got != want {
			t.Errorf("queueControlPlaneURL(%q, false)=%q, want %q", input, got, want)
		}
	}
	// TLS (tlsEnabled=true) — plain addresses get https:// prefix
	for input, want := range map[string]string{
		":8443":                  "https://127.0.0.1:8443",
		"0.0.0.0:8443":          "https://127.0.0.1:8443",
		"apiary.local:8443":     "https://apiary.local:8443",
		// Explicit scheme always wins.
		"https://apiary.example": "https://apiary.example",
		"http://apiary.example":  "http://apiary.example",
	} {
		if got := queueControlPlaneURL(input, true); got != want {
			t.Errorf("queueControlPlaneURL(%q, true)=%q, want %q", input, got, want)
		}
	}
}

func TestSafeWorkerFilename(t *testing.T) {
	if got := safeWorkerFilename("build/us west#1"); got != "build_us_west_1" {
		t.Fatalf("got %q", got)
	}
}
