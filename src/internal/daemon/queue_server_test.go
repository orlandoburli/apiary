package daemon

import "testing"

func TestResolveQueueListenAddress(t *testing.T) {
	cases := []struct {
		in          string
		want        string
		nonLoopback bool
	}{
		{":8080", "127.0.0.1:8080", false},
		{"127.0.0.1:8080", "127.0.0.1:8080", false},
		{"::1]:8080", "::1]:8080", false}, // malformed — pass through
		{"localhost:8080", "localhost:8080", false},
		{"[::1]:8080", "[::1]:8080", false},
		{"0.0.0.0:8080", "0.0.0.0:8080", true},
		{"192.168.1.10:9000", "192.168.1.10:9000", true},
		{"0.0.0.0:0", "0.0.0.0:0", true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, nonLoopback := resolveQueueListenAddress(tc.in)
			if got != tc.want || nonLoopback != tc.nonLoopback {
				t.Fatalf("resolveQueueListenAddress(%q) = (%q, %v), want (%q, %v)",
					tc.in, got, nonLoopback, tc.want, tc.nonLoopback)
			}
		})
	}
}
