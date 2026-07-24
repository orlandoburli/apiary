// Package memory implements the persistent agent memory store: a directory of
// plain markdown files holding daemon-wide durable facts (the global tier) and
// per-task working notes (the task tier). Agents write to it through the
// APIARY_MEMORIZE marker (handled by the workflow engine) and read from it via
// the recall sections injected into their prompts plus direct file reads
// (APIARY_MEMORY_DIR). The files are deliberately human-readable and
// hand-editable; MEMORY.md is derived state, regenerated from entry frontmatter
// on every write and on open, so manual edits and deletions self-heal.
package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// IndexFile is the global index at the memory root: one line per entry.
	IndexFile = "MEMORY.md"
	// globalDir holds one markdown file per durable fact, named by slug.
	globalDir = "global"
	// tasksDir holds one append-only notes file per InternalTask, named by task ID.
	tasksDir = "tasks"

	// DefaultMaxEntryBytes caps a single global entry's content.
	DefaultMaxEntryBytes = 16384

	// lastHashPrefix marks the trailing line of a task-notes file recording the
	// hash of the most recent note, so a retry re-emitting identical content is
	// detected and skipped without parsing bullets back out of markdown.
	lastHashPrefix = "<!-- last-note-hash: "
	lastHashSuffix = " -->"
)

// Memory tiers, mirroring config.MemoryTierTask / MemoryTierGlobal (the config
// package cannot be imported here without inverting the dependency direction).
const (
	TierTask   = "task"
	TierGlobal = "global"
)

// slugRe validates global entry names. The name becomes a filename, so this is
// also the path-traversal guard: lowercase kebab-case only, 2–64 chars.
var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,63}$`)

// taskIDRe validates task-note file keys. Task IDs are internal (ULID-style),
// never agent-authored, but the store still refuses anything that could not be
// a plain filename.
var taskIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Entry is one durable global fact: a markdown body plus the provenance
// frontmatter recording who wrote it and when.
type Entry struct {
	Name        string
	Description string
	Content     string
	Agent       string
	Task        string
	Workflow    string
	Created     time.Time
	Updated     time.Time
}

// EntryMeta is the index view of an Entry (no body).
type EntryMeta struct {
	Name        string
	Description string
	Agent       string
	Updated     time.Time
}

// Note is one task-tier working note, appended to the task's notes file with
// provenance and a timestamp.
type Note struct {
	Content  string
	Agent    string
	Workflow string
	Step     string
	At       time.Time
}

// TaskNotes identifies one task-notes file for lifecycle sweeps.
type TaskNotes struct {
	TaskID  string
	ModTime time.Time
}

// Store is the on-disk memory store rooted at a single directory. All writes
// are serialized by an internal mutex (one daemon process owns the root) and
// performed atomically (temp file + rename), so readers — including humans and
// agent subprocesses — never observe a half-written file.
type Store struct {
	root string
	// MaxEntryBytes caps a single entry/note content. Zero means
	// DefaultMaxEntryBytes.
	MaxEntryBytes int

	mu  sync.Mutex
	now func() time.Time
}

// Open prepares the memory root (creating it and its subdirectories when
// missing) and rebuilds the index from the entries on disk, healing any drift
// from hand edits since the last write.
func Open(root string) (*Store, error) {
	for _, dir := range []string{root, filepath.Join(root, globalDir), filepath.Join(root, tasksDir)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("memory: create %s: %w", dir, err)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return nil, fmt.Errorf("memory: chmod %s: %w", dir, err)
		}
	}
	s := &Store{root: root, now: time.Now}
	if err := s.RebuildIndex(); err != nil {
		return nil, err
	}
	return s, nil
}

// Root returns the memory root directory.
func (s *Store) Root() string { return s.root }

func (s *Store) maxEntryBytes() int {
	if s.MaxEntryBytes > 0 {
		return s.MaxEntryBytes
	}
	return DefaultMaxEntryBytes
}

// ValidSlug reports whether name is acceptable as a global entry name.
func ValidSlug(name string) bool { return slugRe.MatchString(name) }

// UpsertGlobal writes (or overwrites) one global entry and regenerates the
// index. An existing entry keeps its original Created timestamp; everything
// else — body, description, provenance, Updated — is replaced. Same name means
// same fact: agents update knowledge by re-emitting the slug.
func (s *Store) UpsertGlobal(e Entry) error {
	if !ValidSlug(e.Name) {
		return fmt.Errorf("memory: invalid entry name %q (want kebab-case slug, 2-64 chars)", e.Name)
	}
	if strings.TrimSpace(e.Description) == "" {
		return fmt.Errorf("memory: entry %q: description is required", e.Name)
	}
	if strings.TrimSpace(e.Content) == "" {
		return fmt.Errorf("memory: entry %q: content is required", e.Name)
	}
	if len(e.Content) > s.maxEntryBytes() {
		return fmt.Errorf("memory: entry %q: content is %d bytes (max %d)", e.Name, len(e.Content), s.maxEntryBytes())
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.root, globalDir, e.Name+".md")
	now := s.now().UTC()
	e.Updated = now
	e.Created = now
	if prev, err := readEntryFile(path); err == nil {
		if !prev.Created.IsZero() {
			e.Created = prev.Created
		}
	}
	if err := atomicWrite(path, renderEntry(e)); err != nil {
		return fmt.Errorf("memory: write entry %q: %w", e.Name, err)
	}
	return s.rebuildIndexLocked()
}

// AppendTaskNote appends one working note to the task's notes file. A note
// whose content matches the previous note exactly (by hash) is skipped, so a
// retried step re-emitting the same APIARY_MEMORIZE block does not duplicate
// the file.
func (s *Store) AppendTaskNote(taskID string, n Note) error {
	if !taskIDRe.MatchString(taskID) {
		return fmt.Errorf("memory: invalid task id %q", taskID)
	}
	if strings.TrimSpace(n.Content) == "" {
		return fmt.Errorf("memory: task %s: note content is required", taskID)
	}
	if len(n.Content) > s.maxEntryBytes() {
		return fmt.Errorf("memory: task %s: note is %d bytes (max %d)", taskID, len(n.Content), s.maxEntryBytes())
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.root, tasksDir, taskID+".md")
	existing, _ := os.ReadFile(path)
	body, lastHash := splitLastHash(string(existing))

	sum := sha256.Sum256([]byte(n.Content))
	hash := hex.EncodeToString(sum[:8])
	if hash == lastHash {
		return nil // identical to the previous note (e.g. a retry) — skip
	}

	at := n.At
	if at.IsZero() {
		at = s.now()
	}
	var b strings.Builder
	b.WriteString(body)
	if body == "" {
		fmt.Fprintf(&b, "# Task Memory — %s\n\n", taskID)
	}
	prov := strings.Trim(strings.Join(nonEmpty(n.Workflow, n.Step), "/"), "/")
	if prov != "" {
		prov = " [" + prov + "]"
	}
	if n.Agent != "" {
		prov += " (" + n.Agent + ")"
	}
	lines := strings.Split(strings.TrimRight(n.Content, "\n"), "\n")
	fmt.Fprintf(&b, "- %s%s %s\n", at.UTC().Format(time.RFC3339), prov, lines[0])
	for _, line := range lines[1:] {
		b.WriteString("  " + line + "\n")
	}
	b.WriteString(lastHashPrefix + hash + lastHashSuffix + "\n")

	if err := atomicWrite(path, b.String()); err != nil {
		return fmt.Errorf("memory: append task note %s: %w", taskID, err)
	}
	return nil
}

// TaskNoteContent returns the rendered notes for one task ("" when none).
func (s *Store) TaskNoteContent(taskID string) string {
	if !taskIDRe.MatchString(taskID) {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(s.root, tasksDir, taskID+".md"))
	if err != nil {
		return ""
	}
	body, _ := splitLastHash(string(data))
	// Drop the heading line; recall renders its own section header.
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) > 0 && strings.HasPrefix(lines[0], "# ") {
		lines = lines[1:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// List returns the index view of every global entry, sorted by name.
func (s *Store) List() ([]EntryMeta, error) {
	entries, err := s.readAllEntries()
	if err != nil {
		return nil, err
	}
	metas := make([]EntryMeta, 0, len(entries))
	for _, e := range entries {
		metas = append(metas, EntryMeta{Name: e.Name, Description: e.Description, Agent: e.Agent, Updated: e.Updated})
	}
	return metas, nil
}

// Read returns one global entry by name.
func (s *Store) Read(name string) (Entry, error) {
	if !ValidSlug(name) {
		return Entry{}, fmt.Errorf("memory: invalid entry name %q", name)
	}
	return readEntryFile(filepath.Join(s.root, globalDir, name+".md"))
}

// Delete removes one global entry and regenerates the index.
func (s *Store) Delete(name string) error {
	if !ValidSlug(name) {
		return fmt.Errorf("memory: invalid entry name %q", name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(filepath.Join(s.root, globalDir, name+".md")); err != nil {
		return fmt.Errorf("memory: delete entry %q: %w", name, err)
	}
	return s.rebuildIndexLocked()
}

// ListTaskNotes returns every task-notes file with its mtime, for the
// retention sweep.
func (s *Store) ListTaskNotes() ([]TaskNotes, error) {
	dirEntries, err := os.ReadDir(filepath.Join(s.root, tasksDir))
	if err != nil {
		return nil, fmt.Errorf("memory: list task notes: %w", err)
	}
	var out []TaskNotes
	for _, de := range dirEntries {
		name, ok := strings.CutSuffix(de.Name(), ".md")
		if !ok || de.IsDir() {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		out = append(out, TaskNotes{TaskID: name, ModTime: info.ModTime()})
	}
	return out, nil
}

// DeleteTaskNotes removes one task's notes file (no-op when absent).
func (s *Store) DeleteTaskNotes(taskID string) error {
	if !taskIDRe.MatchString(taskID) {
		return fmt.Errorf("memory: invalid task id %q", taskID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(filepath.Join(s.root, tasksDir, taskID+".md"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// RebuildIndex regenerates MEMORY.md from the frontmatter of every entry under
// global/. Entries with unparsable frontmatter are skipped (the file itself is
// untouched); a hand-deleted entry simply disappears from the index.
func (s *Store) RebuildIndex() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rebuildIndexLocked()
}

func (s *Store) rebuildIndexLocked() error {
	entries, err := s.readAllEntries()
	if err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# Apiary Memory Index\n\n")
	if len(entries) == 0 {
		b.WriteString("(no entries yet)\n")
	}
	for _, e := range entries {
		fmt.Fprintf(&b, "- %s — %s\n", e.Name, e.Description)
	}
	if err := atomicWrite(filepath.Join(s.root, IndexFile), b.String()); err != nil {
		return fmt.Errorf("memory: write index: %w", err)
	}
	return nil
}

func (s *Store) readAllEntries() ([]Entry, error) {
	dirEntries, err := os.ReadDir(filepath.Join(s.root, globalDir))
	if err != nil {
		return nil, fmt.Errorf("memory: list entries: %w", err)
	}
	var out []Entry
	for _, de := range dirEntries {
		name, ok := strings.CutSuffix(de.Name(), ".md")
		if !ok || de.IsDir() {
			continue
		}
		e, err := readEntryFile(filepath.Join(s.root, globalDir, de.Name()))
		if err != nil {
			continue // hand-edited beyond recognition; skip from the index
		}
		if e.Name == "" {
			e.Name = name
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// RenderRecall renders the prompt sections for the requested tiers: the
// long-term index (never full bodies — agents read those from disk) and the
// task notes for the given lineage (self first, then ancestors). The combined
// output is bounded by budget: task notes drop oldest-first, then ancestor
// files entirely; the index truncates with an explicit count marker. Empty
// tiers produce no output at all, so a fresh store adds zero prompt overhead.
func (s *Store) RenderRecall(taskIDs []string, tiers []string, budget int) string {
	if budget <= 0 {
		budget = 4000
	}
	wantTask, wantGlobal := false, false
	for _, t := range tiers {
		switch t {
		case TierTask:
			wantTask = true
		case TierGlobal:
			wantGlobal = true
		}
	}

	var global string
	if wantGlobal {
		global = s.renderGlobalIndex(budget / 2)
	}
	var task string
	if wantTask {
		remaining := budget - len(global)
		if remaining > 0 {
			task = s.renderTaskNotes(taskIDs, remaining)
		}
	}

	switch {
	case global == "" && task == "":
		return ""
	case global == "":
		return task
	case task == "":
		return global
	default:
		return global + "\n" + task
	}
}

func (s *Store) renderGlobalIndex(budget int) string {
	metas, err := s.List()
	if err != nil || len(metas) == 0 {
		return ""
	}
	header := "[Long-term Memory]\n" +
		"Persistent memory lives at $APIARY_MEMORY_DIR. The index is below; read the full\n" +
		"entry from $APIARY_MEMORY_DIR/global/<name>.md when one looks relevant. To save a\n" +
		"durable fact for future tasks, emit an APIARY_MEMORIZE block (scope \"global\",\n" +
		"kebab-case name, one-line description, markdown content). Never memorize secrets.\n"
	var b strings.Builder
	b.WriteString(header)
	shown := 0
	for _, m := range metas {
		line := fmt.Sprintf("- %s — %s\n", m.Name, m.Description)
		if b.Len()+len(line) > budget {
			break
		}
		b.WriteString(line)
		shown++
	}
	if shown < len(metas) {
		fmt.Fprintf(&b, "(… %d more entries — read %s)\n", len(metas)-shown, IndexFile)
	}
	return b.String()
}

func (s *Store) renderTaskNotes(taskIDs []string, budget int) string {
	type block struct {
		id    string
		notes string
	}
	var blocks []block
	for _, id := range taskIDs {
		if notes := s.TaskNoteContent(id); notes != "" {
			blocks = append(blocks, block{id, notes})
		}
	}
	if len(blocks) == 0 {
		return ""
	}

	render := func(bs []block) string {
		var b strings.Builder
		b.WriteString("[Task Memory]\n")
		for i, blk := range bs {
			if i > 0 {
				fmt.Fprintf(&b, "(from parent task %s)\n", blk.id)
			}
			b.WriteString(blk.notes + "\n")
		}
		return b.String()
	}

	// Drop ancestors' blocks last-first (self is index 0 and is kept longest),
	// then trim the self block's oldest lines if it alone is still over budget.
	for keep := len(blocks); keep >= 1; keep-- {
		out := render(blocks[:keep])
		if len(out) <= budget {
			return out
		}
	}
	lines := strings.Split(blocks[0].notes, "\n")
	for len(lines) > 1 {
		lines = lines[1:] // oldest notes are at the top
		out := render([]block{{blocks[0].id, strings.Join(lines, "\n")}})
		if len(out) <= budget {
			return out
		}
	}
	return ""
}

// ── file format helpers ──────────────────────────────────────────────

// renderEntry serializes an Entry as frontmatter + body. The frontmatter is a
// minimal key/value block (no nested YAML), parsed back by parseEntry.
func renderEntry(e Entry) string {
	var b strings.Builder
	b.WriteString("---\n")
	writeFM(&b, "name", e.Name)
	writeFM(&b, "description", oneLine(e.Description))
	writeFM(&b, "created", e.Created.UTC().Format(time.RFC3339))
	writeFM(&b, "updated", e.Updated.UTC().Format(time.RFC3339))
	writeFM(&b, "agent", e.Agent)
	writeFM(&b, "task", e.Task)
	writeFM(&b, "workflow", e.Workflow)
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimRight(e.Content, "\n") + "\n")
	return b.String()
}

func writeFM(b *strings.Builder, key, value string) {
	if value == "" {
		return
	}
	fmt.Fprintf(b, "%s: %s\n", key, value)
}

func readEntryFile(path string) (Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Entry{}, err
	}
	return parseEntry(string(data))
}

func parseEntry(raw string) (Entry, error) {
	rest, ok := strings.CutPrefix(raw, "---\n")
	if !ok {
		return Entry{}, fmt.Errorf("memory: entry has no frontmatter")
	}
	fm, body, ok := strings.Cut(rest, "\n---")
	if !ok {
		return Entry{}, fmt.Errorf("memory: entry frontmatter is unterminated")
	}
	var e Entry
	for _, line := range strings.Split(fm, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "name":
			e.Name = value
		case "description":
			e.Description = value
		case "agent":
			e.Agent = value
		case "task":
			e.Task = value
		case "workflow":
			e.Workflow = value
		case "created":
			e.Created, _ = time.Parse(time.RFC3339, value)
		case "updated":
			e.Updated, _ = time.Parse(time.RFC3339, value)
		}
	}
	e.Content = strings.TrimSpace(strings.TrimPrefix(body, "\n"))
	return e, nil
}

// splitLastHash separates a task-notes file into its body and the trailing
// last-note-hash marker ("" when absent).
func splitLastHash(raw string) (body, hash string) {
	trimmed := strings.TrimRight(raw, "\n")
	idx := strings.LastIndex(trimmed, lastHashPrefix)
	if idx < 0 {
		return raw, ""
	}
	line := trimmed[idx:]
	if !strings.HasSuffix(line, lastHashSuffix) || strings.Contains(line, "\n") {
		return raw, ""
	}
	hash = strings.TrimSuffix(strings.TrimPrefix(line, lastHashPrefix), lastHashSuffix)
	return trimmed[:idx], hash
}

// atomicWrite writes content to path via a temp file + rename in the same
// directory, so concurrent readers never see a partial file.
func atomicWrite(path, content string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

func nonEmpty(vals ...string) []string {
	out := vals[:0]
	for _, v := range vals {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
