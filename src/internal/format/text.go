package format

// TruncateMiddle shortens s to at most max runes by eliding the middle rather
// than the tail. Head-preserving truncation (the `truncate` helpers in
// internal/cli and internal/dashboard) is wrong for references shaped like
// "<id>@<instant>": every occurrence of the same routine shares the prefix,
// so cutting the tail keeps the one part that never distinguishes two rows
// and drops the timestamp that does (issue #472).
//
// The kept tail is deliberately longer than the kept head — a bit more than
// half of the surviving runes — since the distinguishing suffix is usually
// the part worth keeping legible (an occurrence timestamp, an ULID's low
// bits, and so on). A string already at or under max is returned unchanged.
func TruncateMiddle(s string, max int) string {
	if max < 1 {
		max = 1
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	keep := max - 1 // one rune spent on the ellipsis
	head := keep / 3
	tail := keep - head
	return string(r[:head]) + "…" + string(r[len(r)-tail:])
}
