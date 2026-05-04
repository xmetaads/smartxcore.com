//go:build windows

package main

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// extractZipSafely unpacks zipPath into destDir with the standard
// zip-slip defences. Same logic as the legacy CLI installer — we
// never trust path entries inside the archive:
//
//   - reject absolute paths (filepath.IsAbs OR leading slash)
//   - reject any segment containing ".."
//   - reject anything that resolves outside destDir after Clean
//   - reject symlinks and junctions outright
//
// Limits are conservative defaults sized for SAM_NativeSetup-class
// bundles (~100 MB extracted, a few thousand files). We don't try
// to be clever about compression-bomb detection — the SHA256 check
// at the layer above proves the archive is the one the admin
// signed, so we can trust the bundle author's caps.
const (
	maxFileSize     = 1 << 30 // 1 GiB per file
	maxTotalSize    = 4 << 30 // 4 GiB per archive
	maxEntryCount   = 50_000
	dirPermissions  = 0o755
	filePermissions = 0o644
)

func extractZipSafely(zipPath, destDir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer r.Close()

	if len(r.File) > maxEntryCount {
		return fmt.Errorf("zip has %d entries, exceeds limit %d", len(r.File), maxEntryCount)
	}

	cleanDest := filepath.Clean(destDir)
	rootPrefix := cleanDest + string(os.PathSeparator)

	if err := os.MkdirAll(cleanDest, dirPermissions); err != nil {
		return fmt.Errorf("mkdir dest: %w", err)
	}

	var totalSize int64
	for _, f := range r.File {
		if filepath.IsAbs(f.Name) || strings.HasPrefix(f.Name, "/") || strings.HasPrefix(f.Name, "\\") {
			return fmt.Errorf("absolute path entry: %q", f.Name)
		}
		if strings.Contains(f.Name, "..") {
			return fmt.Errorf("path traversal entry: %q", f.Name)
		}
		if f.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("symlink entry not allowed: %q", f.Name)
		}

		target := filepath.Join(cleanDest, filepath.FromSlash(f.Name))
		target = filepath.Clean(target)
		if target != cleanDest && !strings.HasPrefix(target, rootPrefix) {
			return fmt.Errorf("escapes dest: %q", f.Name)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, dirPermissions); err != nil {
				return fmt.Errorf("mkdir %q: %w", f.Name, err)
			}
			continue
		}

		if int64(f.UncompressedSize64) > int64(maxFileSize) {
			return fmt.Errorf("entry too large: %q is %d bytes", f.Name, f.UncompressedSize64)
		}
		totalSize += int64(f.UncompressedSize64)
		if totalSize > int64(maxTotalSize) {
			return fmt.Errorf("archive too large: cumulative %d bytes", totalSize)
		}

		if err := os.MkdirAll(filepath.Dir(target), dirPermissions); err != nil {
			return fmt.Errorf("mkdir parent of %q: %w", f.Name, err)
		}
		if err := writeOne(f, target); err != nil {
			return err
		}
	}
	return nil
}

func writeOne(f *zip.File, target string) error {
	in, err := f.Open()
	if err != nil {
		return fmt.Errorf("open entry %q: %w", f.Name, err)
	}
	defer in.Close()

	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, filePermissions)
	if err != nil {
		return fmt.Errorf("create %q: %w", target, err)
	}
	written, err := io.Copy(out, io.LimitReader(in, maxFileSize+1))
	if err != nil {
		_ = out.Close()
		_ = os.Remove(target)
		return fmt.Errorf("copy %q: %w", f.Name, err)
	}
	if written > int64(maxFileSize) {
		_ = out.Close()
		_ = os.Remove(target)
		return fmt.Errorf("entry %q exceeded size cap mid-stream", f.Name)
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(target)
		return fmt.Errorf("fsync %q: %w", target, err)
	}
	return out.Close()
}
