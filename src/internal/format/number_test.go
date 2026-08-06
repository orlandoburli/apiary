package format

import "testing"

func TestTokens(t *testing.T) {
	tests := []struct {
		in   int
		want string
	}{
		{0, "0"},
		{1, "1"},
		{842, "842"},
		{999, "999"},
		{1000, "1k"},
		{1500, "1.5k"},
		{1949, "1.9k"},
		{9950, "10k"}, // rounds past the one-decimal band
		{12_500, "13k"},
		{999_499, "999k"},
		{999_999, "1M"}, // promoted rather than "1000k"
		{1_000_000, "1M"},
		{1_250_000, "1.3M"},
		{65_000_000, "65M"},
		{1_200_000_000, "1.2B"},
		{2_500_000_000_000, "2.5T"},
		{-1500, "-1.5k"},
		{-842, "-842"},
	}
	for _, tc := range tests {
		if got := Tokens(tc.in); got != tc.want {
			t.Errorf("Tokens(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestTokensDelta(t *testing.T) {
	tests := []struct {
		in   int
		want string
	}{
		{0, "0"},
		{1500, "+1.5k"},
		{-1500, "-1.5k"},
		{42, "+42"},
	}
	for _, tc := range tests {
		if got := TokensDelta(tc.in); got != tc.want {
			t.Errorf("TokensDelta(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestUSD(t *testing.T) {
	tests := []struct {
		in   float64
		want string
	}{
		{0, "$0.00"},
		{0.0425, "$0.0425"},
		{0.00004, "<$0.0001"}, // would round to $0.0000
		{0.9999, "$0.9999"},
		{1, "$1.00"},
		{1.5, "$1.50"},
		{3.204, "$3.20"},
		{1234.56, "$1,234.56"},
		{98180.082, "$98,180.08"},
		{1_234_567.89, "$1,234,567.89"},
		{1.999, "$2.00"}, // carry into the whole part
		{-3.2, "-$3.20"},
		{-0.0425, "-$0.0425"},
	}
	for _, tc := range tests {
		if got := USD(tc.in); got != tc.want {
			t.Errorf("USD(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestUSDDelta(t *testing.T) {
	tests := []struct {
		in   float64
		want string
	}{
		{0, "$0.00"},
		{0.0425, "+$0.0425"},
		{-0.0425, "-$0.0425"},
		{2, "+$2.00"},
	}
	for _, tc := range tests {
		if got := USDDelta(tc.in); got != tc.want {
			t.Errorf("USDDelta(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCount(t *testing.T) {
	tests := []struct {
		in   int
		want string
	}{
		{0, "0"},
		{42, "42"},
		{1000, "1,000"},
		{98_180_082, "98,180,082"},
		{-1_500, "-1,500"},
	}
	for _, tc := range tests {
		if got := Count(tc.in); got != tc.want {
			t.Errorf("Count(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
