package format

import "testing"

func TestTruncateMiddle(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{
			name: "short reference unaffected",
			in:   "#412",
			max:  40,
			want: "#412",
		},
		{
			name: "exactly at cap unaffected",
			in:   "0123456789",
			max:  10,
			want: "0123456789",
		},
		{
			name: "long reference elided in middle, suffix preserved",
			in:   "pr-watch@2026-09-01T22:05:00Z",
			max:  20,
			want: "pr-wat…-01T22:05:00Z",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := TruncateMiddle(tc.in, tc.max)
			if got != tc.want {
				t.Fatalf("TruncateMiddle(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
			}
			if len([]rune(got)) > tc.max {
				t.Fatalf("TruncateMiddle(%q, %d) = %q, exceeds max width", tc.in, tc.max, got)
			}
		})
	}
}
