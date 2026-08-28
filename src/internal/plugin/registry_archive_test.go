package plugin

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tarEntry is one member of a synthetic archive, including the shapes a plugin
// archive must never contain.
type tarEntry struct {
	name     string
	body     string
	mode     int64
	typeflag byte
	linkname string
}

func writeTarGz(t *testing.T, entries []tarEntry) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plugin.tar.gz")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	writer := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		flag := entry.typeflag
		if flag == 0 {
			flag = tar.TypeReg
		}
		mode := entry.mode
		if mode == 0 {
			mode = 0o644
		}
		header := &tar.Header{Name: entry.name, Mode: mode, Size: int64(len(entry.body)), Typeflag: flag, Linkname: entry.linkname}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(entry.body)); err != nil {
			t.Fatal(err)
		}
	}
	for _, closer := range []func() error{writer.Close, gzipWriter.Close, file.Close} {
		if err := closer(); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

const testManifestJSON = `{"schema_version":1,"id":"dev.apiary.example","version":"1.0.0","apiary":">= 0.1.0-0","protocol":1,"executable":"plugin.sh","capabilities":["source"]}`

func TestExtractArchiveHappyPath(t *testing.T) {
	archive := writeTarGz(t, []tarEntry{
		{name: ManifestFilename, body: testManifestJSON},
		{name: "plugin.sh", body: "#!/bin/sh\nexit 0\n", mode: 0o755},
	})
	dest := t.TempDir()
	root, err := ExtractArchive(archive, dest)
	if err != nil {
		t.Fatal(err)
	}
	if root != dest {
		t.Fatalf("want the manifest at the archive root, got %s", root)
	}
	if _, err := Load(root, ""); err != nil {
		t.Fatalf("the unpacked plugin should load: %v", err)
	}
}

func TestExtractArchiveFindsSingleTopLevelDirectory(t *testing.T) {
	archive := writeTarGz(t, []tarEntry{
		{name: "plugin-1.0.0/", typeflag: tar.TypeDir, mode: 0o755},
		{name: "plugin-1.0.0/" + ManifestFilename, body: testManifestJSON},
		{name: "plugin-1.0.0/plugin.sh", body: "#!/bin/sh\nexit 0\n", mode: 0o755},
	})
	root, err := ExtractArchive(archive, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(root) != "plugin-1.0.0" {
		t.Fatalf("want the wrapped directory, got %s", root)
	}
}

// An archive is untrusted input handled just before we are asked to trust its
// contents. Everything that could write outside the destination, or smuggle in
// something that is not a plain file, is refused.
func TestExtractArchiveRefusesUnsafeEntries(t *testing.T) {
	cases := []struct {
		name  string
		entry tarEntry
	}{
		{"traversal", tarEntry{name: "../escaped", body: "x"}},
		{"nested traversal", tarEntry{name: "a/../../escaped", body: "x"}},
		{"absolute path", tarEntry{name: "/etc/passwd", body: "x"}},
		{"symlink", tarEntry{name: "link", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"}},
		{"hardlink", tarEntry{name: "link", typeflag: tar.TypeLink, linkname: "plugin.sh"}},
		{"fifo", tarEntry{name: "pipe", typeflag: tar.TypeFifo}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			archive := writeTarGz(t, []tarEntry{{name: ManifestFilename, body: testManifestJSON}, testCase.entry})
			dest := t.TempDir()
			if _, err := ExtractArchive(archive, dest); err == nil {
				t.Fatalf("%s must be refused", testCase.name)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(dest), "escaped")); err == nil {
				t.Fatal("an entry escaped the destination directory")
			}
		})
	}
}

func TestExtractArchiveRejectsUnknownFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plugin.rar")
	if err := os.WriteFile(path, []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ExtractArchive(path, t.TempDir()); err == nil || !strings.Contains(err.Error(), "unsupported archive format") {
		t.Fatalf("want an unsupported-format error, got %v", err)
	}
}

// Format detection reads the bytes, not the filename: release URLs routinely
// carry no usable extension.
func TestExtractArchiveIgnoresTheFilename(t *testing.T) {
	archive := writeTarGz(t, []tarEntry{
		{name: ManifestFilename, body: testManifestJSON},
		{name: "plugin.sh", body: "#!/bin/sh\nexit 0\n", mode: 0o755},
	})
	extensionless := filepath.Join(t.TempDir(), "download")
	body, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(extensionless, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ExtractArchive(extensionless, t.TempDir()); err != nil {
		t.Fatalf("a gzipped tarball must extract regardless of its name: %v", err)
	}
}

func TestExtractArchiveRequiresAManifest(t *testing.T) {
	archive := writeTarGz(t, []tarEntry{{name: "README.md", body: "hello"}})
	if _, err := ExtractArchive(archive, t.TempDir()); err == nil || !strings.Contains(err.Error(), ManifestFilename) {
		t.Fatalf("want a missing-manifest error, got %v", err)
	}
}

func TestDigestsMatchAcceptsBothForms(t *testing.T) {
	if !DigestsMatch("sha256:"+testDigest, strings.ToUpper(testDigest)) {
		t.Fatal("prefixed and bare digests must compare equal")
	}
	if DigestsMatch(testDigest, "") || DigestsMatch("garbage", testDigest) {
		t.Fatal("an unusable digest must never compare equal")
	}
}

func TestFileSHA256IsManifestPinShaped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("apiary"), 0o644); err != nil {
		t.Fatal(err)
	}
	digest, err := FileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(digest, "sha256:") {
		t.Fatalf("want a manifest-shaped pin, got %s", digest)
	}
	if _, _, err := normalizeChecksum(digest); err != nil {
		t.Fatalf("the pin must be one the manifest validator accepts: %v", err)
	}
}
