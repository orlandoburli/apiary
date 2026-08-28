package plugin

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// A plugin archive is untrusted input handled by a process that is about to be
// asked to trust its contents, so extraction refuses everything that could
// write outside the destination or smuggle in something that is not a plain
// file: absolute paths, traversal, symlinks, hardlinks, devices, sockets.
const (
	maxArchiveBytes   = 512 << 20 // uncompressed total; a plugin is one executable
	maxArchiveEntries = 4096
)

// ExtractArchive unpacks a .tar.gz or .zip into destDir, which must already
// exist and should be empty. It returns the directory holding the manifest:
// archives that wrap their payload in a single top-level directory are as
// common as those that do not, and either is fine.
func ExtractArchive(archivePath, destDir string) (string, error) {
	destDir, err := filepath.Abs(destDir)
	if err != nil {
		return "", err
	}
	// The format comes from the bytes, not from the name: a download URL is
	// whatever the publisher's release host makes it, and an archive that is
	// really a gzip must not be refused for arriving without an extension.
	format, err := sniffArchiveFormat(archivePath)
	if err != nil {
		return "", err
	}
	switch format {
	case "tar.gz":
		err = extractTarGz(archivePath, destDir)
	case "zip":
		err = extractZip(archivePath, destDir)
	}
	if err != nil {
		return "", err
	}
	return manifestRoot(destDir)
}

// sniffArchiveFormat identifies the archive by its magic bytes.
func sniffArchiveFormat(archivePath string) (string, error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	header := make([]byte, 4)
	n, err := io.ReadFull(file, header)
	if err != nil && n < 4 {
		return "", fmt.Errorf("read %q: %w", filepath.Base(archivePath), err)
	}
	switch {
	case header[0] == 0x1f && header[1] == 0x8b:
		return "tar.gz", nil
	case string(header) == "PK\x03\x04":
		return "zip", nil
	default:
		return "", fmt.Errorf("unsupported archive format for %q; expected a gzipped tarball or a zip", filepath.Base(archivePath))
	}
}

// manifestRoot locates the directory holding apiary-plugin.json — the archive
// root, or a single top-level directory inside it.
func manifestRoot(destDir string) (string, error) {
	if _, err := os.Stat(filepath.Join(destDir, ManifestFilename)); err == nil {
		return destDir, nil
	}
	entries, err := os.ReadDir(destDir)
	if err != nil {
		return "", err
	}
	var directories []string
	for _, entry := range entries {
		if entry.IsDir() {
			directories = append(directories, entry.Name())
		}
	}
	if len(directories) == 1 {
		candidate := filepath.Join(destDir, directories[0])
		if _, err := os.Stat(filepath.Join(candidate, ManifestFilename)); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("archive contains no %s at its root or in a single top-level directory", ManifestFilename)
}

// safeJoin resolves one archive entry's path against the destination, rejecting
// anything that would land outside it.
func safeJoin(destDir, name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("archive entry has an empty name")
	}
	cleaned := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) || cleaned == ".." {
		return "", fmt.Errorf("archive entry %q escapes the destination directory", name)
	}
	target := filepath.Join(destDir, cleaned)
	relative, err := filepath.Rel(destDir, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("archive entry %q escapes the destination directory", name)
	}
	return target, nil
}

func extractTarGz(archivePath, destDir string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("read %q: %w", filepath.Base(archivePath), err)
	}
	defer func() { _ = gzipReader.Close() }()

	reader := tar.NewReader(gzipReader)
	var written, count int64
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read %q: %w", filepath.Base(archivePath), err)
		}
		count++
		if count > maxArchiveEntries {
			return fmt.Errorf("archive has more than %d entries; refusing it", maxArchiveEntries)
		}
		target, err := safeJoin(destDir, header.Name)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			n, err := writeFile(target, reader, os.FileMode(header.Mode).Perm(), maxArchiveBytes-written)
			if err != nil {
				return err
			}
			written += n
		default:
			// Symlinks, hardlinks, devices and the rest have no legitimate place
			// in a plugin archive, and every one of them is a way to reach
			// outside the directory we are about to validate.
			return fmt.Errorf("archive entry %q is not a regular file or directory; refusing it", header.Name)
		}
	}
}

func extractZip(archivePath, destDir string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("read %q: %w", filepath.Base(archivePath), err)
	}
	defer func() { _ = reader.Close() }()
	if len(reader.File) > maxArchiveEntries {
		return fmt.Errorf("archive has more than %d entries; refusing it", maxArchiveEntries)
	}
	var written int64
	for _, entry := range reader.File {
		target, err := safeJoin(destDir, entry.Name)
		if err != nil {
			return err
		}
		info := entry.FileInfo()
		switch {
		case info.IsDir():
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case info.Mode().IsRegular():
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			source, err := entry.Open()
			if err != nil {
				return err
			}
			n, err := writeFile(target, source, info.Mode().Perm(), maxArchiveBytes-written)
			_ = source.Close()
			if err != nil {
				return err
			}
			written += n
		default:
			return fmt.Errorf("archive entry %q is not a regular file or directory; refusing it", entry.Name)
		}
	}
	return nil
}

// writeFile copies one entry, refusing to exceed the archive's total budget —
// a decompression bomb should cost a bounded amount of disk, not all of it.
func writeFile(target string, source io.Reader, mode os.FileMode, budget int64) (int64, error) {
	if budget <= 0 {
		return 0, fmt.Errorf("archive expands beyond %d bytes; refusing it", int64(maxArchiveBytes))
	}
	if mode == 0 {
		mode = 0o644
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return 0, err
	}
	written, err := io.Copy(file, io.LimitReader(source, budget+1))
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return written, err
	}
	if written > budget {
		return written, fmt.Errorf("archive expands beyond %d bytes; refusing it", int64(maxArchiveBytes))
	}
	return written, nil
}

// FileSHA256 returns a file's digest as "sha256:<hex>", the form the manifest's
// checksum pin uses.
func FileSHA256(path string) (string, error) {
	sum, err := fileSHA256(path)
	if err != nil {
		return "", err
	}
	return "sha256:" + sum, nil
}

// DigestsMatch compares two SHA-256 digests written in either accepted form
// ("sha256:<hex>" or bare hex).
func DigestsMatch(left, right string) bool {
	normalizedLeft, okLeft, errLeft := normalizeChecksum(left)
	normalizedRight, okRight, errRight := normalizeChecksum(right)
	if errLeft != nil || errRight != nil || !okLeft || !okRight {
		return false
	}
	return normalizedLeft == normalizedRight
}
