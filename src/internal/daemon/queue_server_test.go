package daemon

import (
	"testing"
)

func TestResolveQueueListenAddress(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		// Bare port: host defaults to 127.0.0.1
		{":8080", "127.0.0.1:8080"},
		{":0", "127.0.0.1:0"},
		// Explicit loopback — kept as-is
		{"127.0.0.1:9000", "127.0.0.1:9000"},
		{"[::1]:9000", "[::1]:9000"},
		{"localhost:9000", "localhost:9000"},
		// Non-loopback — kept as-is (warning is emitted, not testable without log capture)
		{"0.0.0.0:8080", "0.0.0.0:8080"},
		{"192.168.1.1:8080", "192.168.1.1:8080"},
		// Unparseable — returned unchanged
		{"not-valid", "not-valid"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got := resolveQueueListenAddress(tc.input)
			if got != tc.want {
				t.Errorf("resolveQueueListenAddress(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
