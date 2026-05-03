//go:build windows

package aiupdate

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// extractZipSafely unpacks a ZIP archive into destDir, refusing any
// entry that would write outside destDir, points at a symlink, or
// uses an absolute path. The defences here are the standard
// "zip-slip" mitigations every archive extractor on Windows needs;
// without them a poisoned archive could drop a payload anywhere on
// disk just by claiming a path like "..\..\..\Windows\foo.dll".
//
// We deliberately do NOT scan file CONTENTS — that is the admin's
// job at preflight time on the dashboard. The agent trusts the
// SHA256 it verified before calling us; the only thing we don't
// trust at this layer is the path layout inside the archive.
//
// Layout decisions:
//
//   - destDir must already exist and be empty (or be created by us).
//     We do NOT create it ourselves; caller passes a fresh
//     <ai_root>/<sha>/ directory.
//   - Empty directory entries are honoured (created with 0o700) so
//     things like an empty "logs/" folder the AI expects survive
//     extraction.
//   - File mode is forced to 0o700 — Windows ignores the executable
//     bit anyway and we don't want world-readable surprises.
//
// Limits enforced:
//
//   - Max single-file size: 1 GiB. Defense against zip-bombs that
//     claim 1 KiB compressed but expand to 100 GiB.
//   - Max total uncompressed size: 4 GiB. Same reason at the
//     archive level.
//   - Max file count: 50,000. A 100k-entries archive almost
//     certainly isn't a legitimate Python distribution.
//
// Path safety rules:
//
//   - filepath.IsAbs(name) → reject. ZIPs MUST use relative paths.
//   - "../" or "..\" anywhere in the cleaned target → reject.
//   - Resolved target must lie under destDir (HasPrefix check
//     against the cleaned destDir + os.PathSeparator).
//   - ZIP entry's mode reports a symlink → skip. We never follow
//     or create symlinks during extraction.
const (
	maxFileSize    = 1 << 30 // 1 GiB per file
	maxTotalSize   = 4 << 30 // 4 GiB per archive
	maxEntryCount  = 50_000
	dirPermissions = 0o700
	filePermissions = 0o700
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
		// Reject absolute paths at the source. zip.Reader's f.Name
		// is the raw entry; we need to defend against both Windows
		// "C:\foo" and Unix "/foo" forms.
		if filepath.IsAbs(f.Name) || strings.HasPrefix(f.Name, "/") || strings.HasPrefix(f.Name, "\\") {
			return fmt.Errorf("absolute path entry: %q", f.Name)
		}

		// Reject any traversal segments.
		if strings.Contains(f.Name, "..") {
			return fmt.Errorf("path traversal entry: %q", f.Name)
		}

		// Reject symlinks — Windows symlink creation needs admin
		// or developer-mode anyway, and a symlink in a ZIP is a
		// classic privilege-escalation primitive.
		if f.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("symlink entry not allowed: %q", f.Name)
		}

		// Resolve to the real on-disk target and verify it lands
		// under destDir.
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

		// Cap per-entry size.
		if int64(f.UncompressedSize64) > int64(maxFileSize) {
			return fmt.Errorf("entry too large: %q is %d bytes (cap %d)", f.Name, f.UncompressedSize64, maxFileSize)
		}
		totalSize += int64(f.UncompressedSize64)
		if totalSize > int64(maxTotalSize) {
			return fmt.Errorf("archive too large: cumulative %d bytes (cap %d)", totalSize, maxTotalSize)
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

// writeOne extracts a single zip.File entry to a local path. Caps
// io.Copy to maxFileSize so a malicious archive that lies about its
// uncompressed size in the central directory still can't write
// past our limit.
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
	if err := out.Close(); err != nil {
		_ = os.Remove(target)
		return err
	}
	return nil
}

// installZip extracts the verified ZIP at tmpZip into a fresh
// <aiRoot>/extracted/ tree, atomically replacing whatever was
// there. Returns the absolute path to the extracted root and the
// normalised relative entrypoint within it. The marker writer
// joins those two to produce the launcher's spawn path.
//
// Crash safety: stage to <aiRoot>/extracted.staging/ first; if
// extraction fails halfway the old <aiRoot>/extracted/ is
// untouched. If extraction succeeds we rename old → .old, new →
// extracted, then RemoveAll the .old. Two intermediate states
// could be visible to a concurrent crash, both recoverable on
// next tick (the leftover dirs are wiped at start of every
// install).
//
// entrypointRaw is the relative path the admin entered in
// dashboard, e.g. "SAM_NativeSetup\\S.A.M_Enterprise_Agent_Setup_Native.exe"
// or "SAM_NativeSetup/S.A.M_Enterprise_Agent_Setup_Native.exe".
// We normalise to OS path separators and validate that it stays
// under the extracted tree (no traversal segments, no abs paths).
func (u *Updater) installZip(tmpZip, aiRoot, entrypointRaw string) (extractedDir, entrypointRel string, err error) {
	extracted := filepath.Join(aiRoot, "extracted")
	staging := filepath.Join(aiRoot, "extracted.staging")
	old := filepath.Join(aiRoot, "extracted.old")

	// Wipe any leftover from a prior crashed run.
	_ = os.RemoveAll(staging)
	_ = os.RemoveAll(old)

	if err := extractZipSafely(tmpZip, staging); err != nil {
		_ = os.RemoveAll(staging)
		return "", "", fmt.Errorf("extract: %w", err)
	}

	// Validate + normalise entrypoint relative path.
	ep := strings.TrimSpace(entrypointRaw)
	if ep == "" {
		_ = os.RemoveAll(staging)
		return "", "", fmt.Errorf("zip archive_format requires entrypoint")
	}
	ep = filepath.FromSlash(ep)
	if filepath.IsAbs(ep) || strings.Contains(ep, "..") {
		_ = os.RemoveAll(staging)
		return "", "", fmt.Errorf("invalid entrypoint path: %q", entrypointRaw)
	}
	check := filepath.Join(staging, ep)
	if st, err := os.Stat(check); err != nil || st.IsDir() {
		_ = os.RemoveAll(staging)
		return "", "", fmt.Errorf("entrypoint %q not found inside archive", entrypointRaw)
	}

	// Rename current → old (only if exists).
	if _, err := os.Stat(extracted); err == nil {
		if err := os.Rename(extracted, old); err != nil {
			_ = os.RemoveAll(staging)
			return "", "", fmt.Errorf("move old aside: %w", err)
		}
	}

	if err := os.Rename(staging, extracted); err != nil {
		// Roll back: try to put old back where it was so the
		// launcher still has something to spawn on its next tick.
		if _, errStat := os.Stat(old); errStat == nil {
			_ = os.Rename(old, extracted)
		}
		_ = os.RemoveAll(staging)
		return "", "", fmt.Errorf("install new: %w", err)
	}

	_ = os.RemoveAll(old)
	return extracted, ep, nil
}

// defaultStr returns fallback if s is empty.
func defaultStr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// suppress unused-import warning when no callers reference these
// helpers at the moment but the file still imports them.
var _ = errors.New

