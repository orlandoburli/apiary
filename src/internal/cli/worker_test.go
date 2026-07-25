package cli

import "testing"

func TestQueueControlPlaneURL(t *testing.T) {
	tests := []struct {
		input      string
		tlsEnabled bool
		want       string
	}{
		{":8080", false, "http://127.0.0.1:8080"},
		{"0.0.0.0:8080", false, "http://127.0.0.1:8080"},
		{"apiary.local:8080", false, "http://apiary.local:8080"},
		{"https://apiary.example", false, "https://apiary.example"},
		{":8080", true, "https://127.0.0.1:8080"},
		{"0.0.0.0:8080", true, "https://127.0.0.1:8080"},
		{"apiary.local:8080", true, "https://apiary.local:8080"},
		{"https://apiary.example", true, "https://apiary.example"},
	}
	for _, tc := range tests {
		if got := queueControlPlaneURL(tc.input, tc.tlsEnabled); got != tc.want {
			t.Errorf("queueControlPlaneURL(%q, %v)=%q, want %q", tc.input, tc.tlsEnabled, got, tc.want)
		}
	}
}

func TestSafeWorkerFilename(t *testing.T) {
	if got := safeWorkerFilename("build/us west#1"); got != "build_us_west_1" {
		t.Fatalf("got %q", got)
	}
}
