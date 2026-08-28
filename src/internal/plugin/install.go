package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Installing is still placing files, deliberately — the registry only removes
// the parts a human does badly: picking the right artifact, and checking it is
// the one the publisher shipped. Nothing here executes plugin code, nothing
// lands in a searched directory until every check has passed, and nothing is
// ever enabled: that stays an edit the operator makes to apiary.yaml.
const (
	maxArtifactBytes   = 256 << 20
	artifactTimeout    = 10 * time.Minute
	incomingDirPattern = ".incoming-*"
)

// StageOptions controls a staged install.
type StageOptions struct {
	// ExpectedArchiveSHA256 is an operator-supplied digest (--sha256). It is a
	// cross-check against the registry's, never a replacement for it: if the two
	// disagree, one of them is wrong and the install stops.
	ExpectedArchiveSHA256 string
	Client                *http.Client
	Timeout               time.Duration
}

// Staged is a verified plugin sitting outside every searched directory, ready
// to be committed or discarded.
type Staged struct {
	Resolved  *Resolved
	Installed *Installed
	// Root is the directory that will become <plugin_dir>/<id>.
	Root string
	// PinInjected records that the manifest had no checksum and was pinned to
	// the registry's digest.
	PinInjected bool
	tempRoot    string
}

// Stage runs every step of an install except the last: download, verify the
// archive digest, unpack safely, load and validate the manifest, confirm it is
// the plugin that was asked for, verify the executable digest, and pin it.
func Stage(ctx context.Context, resolved *Resolved, apiaryVersion string, opts StageOptions) (*Staged, error) {
	if resolved == nil || resolved.Artifact == nil {
		return nil, fmt.Errorf("nothing resolved to install")
	}
	if expected := opts.ExpectedArchiveSHA256; expected != "" && !DigestsMatch(expected, resolved.Artifact.ArchiveSHA256) {
		return nil, fmt.Errorf("--sha256 %s does not match the registry's digest %s for %s; one of them is wrong, so nothing was downloaded",
			expected, resolved.Artifact.ArchiveSHA256, resolved.Artifact.URL)
	}
	tempRoot, err := os.MkdirTemp("", "apiary-install-*")
	if err != nil {
		return nil, err
	}
	staged := &Staged{Resolved: resolved, tempRoot: tempRoot}
	if err := staged.fill(ctx, apiaryVersion, opts); err != nil {
		staged.Discard()
		return nil, err
	}
	return staged, nil
}

func (s *Staged) fill(ctx context.Context, apiaryVersion string, opts StageOptions) error {
	artifact := s.Resolved.Artifact
	archive := filepath.Join(s.tempRoot, safeArchiveName(artifact.URL))
	if err := downloadArtifact(ctx, artifact.URL, archive, opts); err != nil {
		return err
	}
	digest, err := FileSHA256(archive)
	if err != nil {
		return err
	}
	if !DigestsMatch(digest, artifact.ArchiveSHA256) {
		// Not a network hiccup: the bytes are not the bytes the registry
		// reviewed, and the only safe move is to stop.
		return fmt.Errorf("checksum mismatch for %s\n  registry declares %s\n  download is       %s\nnothing was installed",
			artifact.URL, normalizedDigest(artifact.ArchiveSHA256), normalizedDigest(digest))
	}

	unpacked := filepath.Join(s.tempRoot, "unpacked")
	if err := os.MkdirAll(unpacked, 0o755); err != nil {
		return err
	}
	root, err := ExtractArchive(archive, unpacked)
	if err != nil {
		return err
	}
	installed, err := Load(root, apiaryVersion)
	if err != nil {
		return err
	}
	// The registry is not authoritative about what is inside the archive; the
	// manifest is. They have to agree, or the listing describes something other
	// than what was downloaded.
	if installed.Manifest.ID != s.Resolved.Plugin.ID {
		return fmt.Errorf("the archive contains plugin %q, but %q was requested; nothing was installed",
			installed.Manifest.ID, s.Resolved.Plugin.ID)
	}
	if installed.Manifest.Version != s.Resolved.Release.Version {
		return fmt.Errorf("the archive contains version %q, but the registry lists %q; nothing was installed",
			installed.Manifest.Version, s.Resolved.Release.Version)
	}
	executable := filepath.Join(root, installed.Manifest.Executable)
	executableDigest, err := FileSHA256(executable)
	if err != nil {
		return err
	}
	if !DigestsMatch(executableDigest, artifact.ExecutableSHA256) {
		return fmt.Errorf("checksum mismatch for the executable inside %s\n  registry declares %s\n  archive contains  %s\nnothing was installed",
			filepath.Base(artifact.URL), normalizedDigest(artifact.ExecutableSHA256), normalizedDigest(executableDigest))
	}

	pinned, err := pinChecksum(installed, executableDigest, apiaryVersion)
	if err != nil {
		return err
	}
	s.Installed, s.PinInjected, s.Root = pinned.installed, pinned.injected, root
	return nil
}

type pinResult struct {
	installed *Installed
	injected  bool
}

// pinChecksum writes the registry's executable digest into the manifest when the
// publisher shipped no pin. This is the point of the whole exercise: an
// unpinned manifest gains a digest that originates in the registry repository
// rather than beside the binary, so the daemon's per-invocation integrity check
// is comparing against a value the publisher cannot quietly rewrite.
func pinChecksum(installed *Installed, executableDigest, apiaryVersion string) (pinResult, error) {
	if existing := installed.Manifest.Checksum; existing != "" {
		if !DigestsMatch(existing, executableDigest) {
			return pinResult{}, fmt.Errorf("the manifest pins checksum %s, which is not the executable it ships (%s); nothing was installed",
				existing, normalizedDigest(executableDigest))
		}
		return pinResult{installed: installed}, nil
	}
	manifest := installed.Manifest
	manifest.Checksum = executableDigest
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return pinResult{}, err
	}
	if err := os.WriteFile(installed.Path, append(encoded, '\n'), 0o644); err != nil {
		return pinResult{}, err
	}
	// Re-load rather than trusting the rewrite: what lands on disk has to be a
	// manifest the daemon itself accepts.
	reloaded, err := Load(installed.Root, apiaryVersion)
	if err != nil {
		return pinResult{}, fmt.Errorf("pinning the checksum produced a manifest Apiary rejects: %w", err)
	}
	return pinResult{installed: reloaded, injected: true}, nil
}

// Commit moves the staged plugin into its final location in one atomic rename.
// Until this call, nothing exists in any searched directory.
func (s *Staged) Commit(target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if err := os.Rename(s.Root, target); err == nil {
		s.tempRoot = ""
		return nil
	}
	// A staging directory on another filesystem cannot be renamed into place, so
	// the payload is copied next to the target and renamed within it. The
	// manifest is written last: a directory without one is invisible to
	// discovery, so a concurrent `apiary validate` never sees a half-copy.
	incoming, err := os.MkdirTemp(filepath.Dir(target), incomingDirPattern)
	if err != nil {
		return err
	}
	if err := copyPluginDir(s.Root, incoming); err != nil {
		_ = os.RemoveAll(incoming)
		return err
	}
	if err := os.Rename(incoming, target); err != nil {
		_ = os.RemoveAll(incoming)
		return err
	}
	return nil
}

// Discard removes everything the staging step created.
func (s *Staged) Discard() {
	if s == nil || s.tempRoot == "" {
		return
	}
	_ = os.RemoveAll(s.tempRoot)
	s.tempRoot = ""
}

// copyPluginDir copies a plugin directory, writing the manifest last so the
// destination is never discoverable while it is incomplete.
func copyPluginDir(source, target string) error {
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	var manifest os.DirEntry
	for _, entry := range entries {
		if entry.Name() == ManifestFilename {
			manifest = entry
			continue
		}
		if err := copyEntry(source, target, entry); err != nil {
			return err
		}
	}
	if manifest == nil {
		return fmt.Errorf("staged plugin has no %s", ManifestFilename)
	}
	return copyEntry(source, target, manifest)
}

func copyEntry(source, target string, entry os.DirEntry) error {
	from, to := filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name())
	info, err := entry.Info()
	if err != nil {
		return err
	}
	if entry.IsDir() {
		if err := os.MkdirAll(to, info.Mode().Perm()); err != nil {
			return err
		}
		nested, err := os.ReadDir(from)
		if err != nil {
			return err
		}
		for _, child := range nested {
			if err := copyEntry(from, to, child); err != nil {
				return err
			}
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("staged plugin contains %q, which is not a regular file", entry.Name())
	}
	reader, err := os.Open(from)
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()
	writer, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, err = io.Copy(writer, reader)
	if closeErr := writer.Close(); err == nil {
		err = closeErr
	}
	return err
}

func downloadArtifact(ctx context.Context, url, target string, opts StageOptions) error {
	if !strings.HasPrefix(url, "https://") {
		return fmt.Errorf("artifact URL %q must be https", url)
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = artifactTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: unexpected status %s", url, response.Status)
	}
	file, err := os.Create(target)
	if err != nil {
		return err
	}
	written, err := io.Copy(file, io.LimitReader(response.Body, maxArtifactBytes+1))
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	if written > maxArtifactBytes {
		return fmt.Errorf("download %s: larger than %d bytes; refusing it", url, int64(maxArtifactBytes))
	}
	return nil
}

// safeArchiveName keeps the download's basename for readable errors without
// letting a registry URL choose a path.
func safeArchiveName(url string) string {
	name := filepath.Base(filepath.FromSlash(strings.TrimSuffix(url, "/")))
	if name == "." || name == string(filepath.Separator) || strings.Contains(name, "..") {
		return "artifact"
	}
	return name
}

func normalizedDigest(digest string) string {
	normalized, ok, err := normalizeChecksum(digest)
	if err != nil || !ok {
		return digest
	}
	return "sha256:" + normalized
}
