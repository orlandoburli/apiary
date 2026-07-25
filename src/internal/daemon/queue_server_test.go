package daemon

import (
	"testing"
)

func TestResolveQueueListenAddress(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{":8080", "127.0.0.1:8080"},
		{":0", "127.0.0.1:0"},
		{"127.0.0.1:8080", "127.0.0.1:8080"},
		{"localhost:8080", "localhost:8080"},
		{"[::1]:8080", "[::1]:8080"},
		// non-loopback passes through unchanged (warning is logged separately)
		{"0.0.0.0:8080", "0.0.0.0:8080"},
		{"192.168.1.1:8080", "192.168.1.1:8080"},
		// unparseable — pass through and let net.Listen report the error
		{"not-an-address", "not-an-address"},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := resolveQueueListenAddress(tc.input)
			if got != tc.want {
				t.Errorf("resolveQueueListenAddress(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
