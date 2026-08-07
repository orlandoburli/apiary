package improve

import (
	"fmt"
	"strings"
)

// Hunk is one contiguous change within a patch.
type Hunk struct {
	// OldStart is the 1-based line in the original file where the hunk begins.
	OldStart int
	OldLines int
	NewStart int
	NewLines int
	// Lines are the raw body lines, each still carrying its leading ' ', '-' or
	// '+'. "\ No newline at end of file" markers are dropped during parsing.
	Lines []string
}

// Patch is a parsed unified diff for a single file.
type Patch struct {
	// Path is the target, taken from the +++ header with any a/ or b/ prefix
	// stripped.
	Path  string
	Hunks []Hunk
}

// ParsePatch reads a unified diff for one file.
//
// It is strict on purpose. A malformed hunk header is rejected rather than
// guessed at, because the alternative — inferring where a change belongs — is
// how a patch silently lands in the wrong place. Observed in practice: an
// advisor emitted `@@ implementation/merge step @@` as a header, naming the
// section in prose instead of giving line numbers. That must fail loudly.
func ParsePatch(diff string) (*Patch, error) {
	lines := strings.Split(strings.ReplaceAll(diff, "\r\n", "\n"), "\n")
	p := &Patch{}
	var current *Hunk
	// Remaining body lines the open hunk expects, from its declared counts.
	oldLeft, newLeft := 0, 0

	for i := 0; i < len(lines); i++ {
		line := lines[i]

		switch {
		case strings.HasPrefix(line, "--- "):
			continue // the old path is informational; +++ names the target

		case strings.HasPrefix(line, "+++ "):
			path := strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
			// Drop a trailing tab-separated timestamp, if present.
			if idx := strings.IndexByte(path, '\t'); idx >= 0 {
				path = path[:idx]
			}
			path = strings.TrimPrefix(strings.TrimPrefix(path, "a/"), "b/")
			if path == "" || path == "/dev/null" {
				return nil, fmt.Errorf("patch targets %q, which is not a file", path)
			}
			p.Path = path

		case strings.HasPrefix(line, "@@"):
			h, err := parseHunkHeader(line)
			if err != nil {
				return nil, err
			}
			p.Hunks = append(p.Hunks, h)
			current = &p.Hunks[len(p.Hunks)-1]
			oldLeft, newLeft = h.OldLines, h.NewLines

		case strings.HasPrefix(line, "diff "), strings.HasPrefix(line, "index "),
			strings.HasPrefix(line, "new file"), strings.HasPrefix(line, "deleted file"),
			strings.HasPrefix(line, "similarity "), strings.HasPrefix(line, "rename "):
			continue

		case strings.HasPrefix(line, `\ No newline`):
			continue

		default:
			// Prose around the diff is tolerated — models routinely wrap a patch
			// in explanation — but only outside a hunk. Inside one, the hunk's
			// declared line counts say exactly how many body lines to consume, so
			// there is no need to guess where it ends. That matters for blank
			// lines: one inside a hunk is an unprefixed context line for a blank
			// source line, while one after the hunk is just spacing before prose,
			// and only the counts distinguish them.
			if current == nil || (oldLeft <= 0 && newLeft <= 0) {
				current = nil
				continue
			}
			if len(line) > 0 && !strings.ContainsAny(line[:1], " -+") {
				current = nil
				continue
			}
			if line == "" {
				line = " "
			}
			switch line[0] {
			case ' ':
				oldLeft--
				newLeft--
			case '-':
				oldLeft--
			case '+':
				newLeft--
			}
			current.Lines = append(current.Lines, line)
		}
	}

	if p.Path == "" {
		return nil, fmt.Errorf("patch has no +++ target header")
	}
	if len(p.Hunks) == 0 {
		return nil, fmt.Errorf("patch for %s has no hunks", p.Path)
	}
	return p, nil
}

// parseHunkHeader parses "@@ -old,count +new,count @@ optional context".
func parseHunkHeader(line string) (Hunk, error) {
	rest := strings.TrimPrefix(line, "@@")
	end := strings.Index(rest, "@@")
	if end < 0 {
		return Hunk{}, fmt.Errorf("malformed hunk header %q: missing closing @@", line)
	}
	spec := strings.Fields(strings.TrimSpace(rest[:end]))
	if len(spec) != 2 {
		return Hunk{}, fmt.Errorf("malformed hunk header %q: want @@ -old,count +new,count @@", line)
	}
	oldStart, oldLines, err := parseRange(spec[0], '-')
	if err != nil {
		return Hunk{}, fmt.Errorf("malformed hunk header %q: %w", line, err)
	}
	newStart, newLines, err := parseRange(spec[1], '+')
	if err != nil {
		return Hunk{}, fmt.Errorf("malformed hunk header %q: %w", line, err)
	}
	return Hunk{OldStart: oldStart, OldLines: oldLines, NewStart: newStart, NewLines: newLines}, nil
}

// parseRange parses "-12,7" or "+12" (count defaults to 1).
func parseRange(s string, sign byte) (start, count int, err error) {
	if len(s) == 0 || s[0] != sign {
		return 0, 0, fmt.Errorf("range %q must begin with %q", s, string(sign))
	}
	body := s[1:]
	count = 1
	if idx := strings.IndexByte(body, ','); idx >= 0 {
		if _, err := fmt.Sscanf(body[idx+1:], "%d", &count); err != nil {
			return 0, 0, fmt.Errorf("range %q has a non-numeric count", s)
		}
		body = body[:idx]
	}
	if _, err := fmt.Sscanf(body, "%d", &start); err != nil {
		return 0, 0, fmt.Errorf("range %q has a non-numeric start", s)
	}
	if start < 0 || count < 0 {
		return 0, 0, fmt.Errorf("range %q is negative", s)
	}
	return start, count, nil
}

// Apply applies the patch to content in memory and returns the result.
//
// Context must match exactly. There is no fuzz factor and no offset search: a
// hunk whose context does not match is an error, not an invitation to look
// nearby. A patch that lands in the wrong place is worse than one that does not
// land at all, because the diff shown to the reviewer would no longer describe
// what the file became.
func (p *Patch) Apply(content string) (string, error) {
	// Preserve whether the file ended with a newline: re-adding one that was not
	// there (or dropping one that was) is a spurious change in the result.
	hadTrailingNewline := strings.HasSuffix(content, "\n")
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	if content == "" {
		lines = nil
	}

	var out []string
	cursor := 0 // 0-based index into lines

	for hi, h := range p.Hunks {
		start := h.OldStart - 1
		if h.OldLines == 0 {
			// A pure insertion is positioned after OldStart.
			start = h.OldStart
		}
		if start < cursor {
			return "", fmt.Errorf("hunk %d starts at line %d, before the previous hunk ended (line %d): hunks must be ordered and must not overlap",
				hi+1, h.OldStart, cursor+1)
		}
		if start > len(lines) {
			return "", fmt.Errorf("hunk %d starts at line %d but the file has %d lines",
				hi+1, h.OldStart, len(lines))
		}

		out = append(out, lines[cursor:start]...)
		cursor = start

		for _, l := range h.Lines {
			if l == "" {
				continue
			}
			marker, text := l[0], l[1:]
			switch marker {
			case ' ':
				if cursor >= len(lines) {
					return "", fmt.Errorf("hunk %d expects context %q at line %d, but the file ends at line %d",
						hi+1, text, cursor+1, len(lines))
				}
				if lines[cursor] != text {
					return "", fmt.Errorf("hunk %d context mismatch at line %d:\n  expected: %q\n  actual:   %q",
						hi+1, cursor+1, text, lines[cursor])
				}
				out = append(out, text)
				cursor++
			case '-':
				if cursor >= len(lines) {
					return "", fmt.Errorf("hunk %d expects to remove %q at line %d, but the file ends at line %d",
						hi+1, text, cursor+1, len(lines))
				}
				if lines[cursor] != text {
					return "", fmt.Errorf("hunk %d cannot remove line %d:\n  expected: %q\n  actual:   %q",
						hi+1, cursor+1, text, lines[cursor])
				}
				cursor++
			case '+':
				out = append(out, text)
			default:
				return "", fmt.Errorf("hunk %d has a body line with an unknown marker %q", hi+1, l)
			}
		}
	}

	out = append(out, lines[cursor:]...)

	result := strings.Join(out, "\n")
	if hadTrailingNewline && result != "" {
		result += "\n"
	}
	return result, nil
}

// Stats reports how many lines the patch adds and removes, for the summary line.
func (p *Patch) Stats() (added, removed int) {
	for _, h := range p.Hunks {
		for _, l := range h.Lines {
			if l == "" {
				continue
			}
			switch l[0] {
			case '+':
				added++
			case '-':
				removed++
			}
		}
	}
	return added, removed
}
