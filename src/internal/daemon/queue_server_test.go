package daemon

import "testing"

func TestIsLoopbackOnly(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8080", true},
		{"[::1]:8080", true},
		{"localhost:8080", true},
		{"LOCALHOST:8080", true},
		{":8080", false},
		{"0.0.0.0:8080", false},
		{"[::]:8080", false},
		{"192.168.1.1:8080", false},
		{"10.0.0.1:9000", false},
		{"not-valid", false},
	}
	for _, tc := range cases {
		if got := isLoopbackOnly(tc.addr); got != tc.want {
			t.Errorf("isLoopbackOnly(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}
