package cli

import "testing"

func TestQueueControlPlaneURL(t *testing.T) {
	for input, want := range map[string]string{
		":8080":                  "http://127.0.0.1:8080",
		"0.0.0.0:8080":           "http://127.0.0.1:8080",
		"apiary.local:8080":      "http://apiary.local:8080",
		"https://apiary.example": "https://apiary.example",
	} {
		if got := queueControlPlaneURL(input); got != want {
			t.Errorf("queueControlPlaneURL(%q)=%q, want %q", input, got, want)
		}
	}
}

func TestSafeWorkerFilename(t *testing.T) {
	if got := safeWorkerFilename("build/us west#1"); got != "build_us_west_1" {
		t.Fatalf("got %q", got)
	}
}
