//go:build windows

// Package aiupdate is the AI bundle installer. Given the metadata
// for an active AI package (URL, SHA256, size, archive_format,
// entrypoint), it:
//
//  1. Skips work if the on-disk .version marker already records
//     that exact SHA + format (idempotent re-run).
//  2. Downloads the bundle from the CDN, using parallel HTTP Range
//     requests when the file is large enough and the origin honours
//     them (Bunny CDN does), falling back to a single streamed read
//     when the origin doesn't.
//  3. Verifies the SHA256 of the bytes that landed on disk against
//     the metadata. Mismatch is fatal — we delete the temp file and
//     report the discrepancy.
//  4. Installs the bundle. For archive_format=zip we extract via
//     installZip into <aiRoot>/extracted/ with full zip-slip
//     defences. For archive_format=exe (legacy) we atomic-rename
//     into <aiRoot>/ai-client.exe.
//  5. Writes the .version marker recording where the launcher
//     should spawn the entrypoint.
//
// SHA256 verification is what makes the public-CDN download safe:
// even though anyone can pull bytes from the CDN URL, an attacker
// who substituted bytes upstream would fail the hash check and the
// install would abort.
package aiupdate

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/rs/zerolog/log"

	"github.com/worktrack/agent/internal/api"
)

// Installer downloads + verifies + installs the active AI bundle.
// One-shot: caller invokes InstallOnce(ctx, meta) once per
// Smartcore.exe execution.
type Installer struct {
	client *api.Client
	aiRoot string // %LOCALAPPDATA%\Smartcore\ai

	mu sync.Mutex // guards InstallOnce against concurrent callers
}

// NewInstaller returns an Installer rooted at aiRoot. aiRoot is
// created if missing on first install.
func NewInstaller(client *api.Client, aiRoot string) *Installer {
	return &Installer{client: client, aiRoot: aiRoot}
}

// InstallOnce verifies + downloads + extracts the active AI bundle
// described by meta. Idempotent: if the on-disk marker already
// matches meta.SHA256 + meta.ArchiveFormat, returns immediately.
//
// Caller (cmd/agent/main.go) is expected to read the .version
// marker afterwards to discover the resolved spawn path.
func (u *Installer) InstallOnce(ctx context.Context, meta *api.AIPackageInfo) error {
	if meta == nil {
		return errors.New("nil meta")
	}
	if meta.URL == "" || meta.SHA256 == "" {
		return errors.New("meta missing url or sha256")
	}

	u.mu.Lock()
	defer u.mu.Unlock()

	if err := os.MkdirAll(u.aiRoot, 0o755); err != nil {
		return fmt.Errorf("mkdir ai root: %w", err)
	}

	// Fast path: marker already records this SHA + format. The
	// caller will spawn the existing entrypoint without re-extracting.
	expectedFormat := defaultStr(meta.ArchiveFormat, "exe")
	if marker, _ := ReadMarker(u.aiRoot); marker.SHA256 != "" && marker.SHA256 == meta.SHA256 && marker.ArchiveFormat == expectedFormat {
		log.Info().
			Str("sha256", trim(meta.SHA256)).
			Str("version", marker.Version).
			Msg("AI bundle already installed (marker hit)")
		return nil
	}

	// Slow path for 'exe' format only: hash the file already on disk.
	// Saves a re-download when the marker is missing but the file is
	// the right one (recovery from a crashed prior run).
	target := u.localPath()
	if expectedFormat != "zip" {
		localSHA, _ := hashFile(target)
		if localSHA == meta.SHA256 {
			_ = WriteMarker(u.aiRoot, Marker{
				Version:       meta.VersionLabel,
				SHA256:        meta.SHA256,
				ArchiveFormat: "exe",
				SpawnPath:     target,
				SpawnCWD:      u.aiRoot,
			})
			log.Info().Str("sha256", trim(meta.SHA256)).Msg("AI bundle already on disk (rehash matched marker rewritten)")
			return nil
		}
	}

	log.Info().
		Str("to_sha", trim(meta.SHA256)).
		Str("version", meta.VersionLabel).
		Str("format", expectedFormat).
		Int64("bytes", meta.SizeBytes).
		Msg("downloading AI bundle")

	// Refuse early if the volume can't even hold 2x the advertised
	// size (download tmp + room for old artefacts).
	if meta.SizeBytes > 0 {
		if err := ensureFreeSpace(filepath.Dir(target), meta.SizeBytes*2); err != nil {
			return err
		}
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	tmp := target + ".new"
	_ = os.Remove(tmp)

	dlCtx, dlCancel := context.WithTimeout(ctx, 10*time.Minute)
	defer dlCancel()

	dlStart := time.Now()
	written, err := u.downloadFile(dlCtx, meta.URL, meta.SizeBytes, tmp)
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("download: %w", err)
	}

	got, err := hashFile(tmp)
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("hash tmp: %w", err)
	}
	if got != meta.SHA256 {
		_ = os.Remove(tmp)
		return fmt.Errorf("sha256 mismatch: want %s got %s", meta.SHA256, got)
	}

	var spawnPath, spawnCWD string
	if expectedFormat == "zip" {
		extracted, entrypoint, err := u.installZip(tmp, u.aiRoot, meta.Entrypoint)
		if err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("install zip: %w", err)
		}
		_ = os.Remove(tmp)
		spawnPath = filepath.Join(extracted, entrypoint)
		spawnCWD = filepath.Dir(spawnPath)
	} else {
		// Atomic replace single-file 'exe' format.
		if _, err := os.Stat(target); err == nil {
			old := target + ".old"
			_ = os.Remove(old)
			if err := os.Rename(target, old); err != nil {
				_ = os.Remove(tmp)
				return fmt.Errorf("move old aside: %w", err)
			}
			_ = os.Remove(old)
		}
		if err := os.Rename(tmp, target); err != nil {
			return fmt.Errorf("install new: %w", err)
		}
		spawnPath = target
		spawnCWD = u.aiRoot
	}

	if err := WriteMarker(u.aiRoot, Marker{
		Version:       meta.VersionLabel,
		SHA256:        meta.SHA256,
		ArchiveFormat: expectedFormat,
		SpawnPath:     spawnPath,
		SpawnCWD:      spawnCWD,
	}); err != nil {
		log.Warn().Err(err).Msg("write marker")
	}

	dur := time.Since(dlStart)
	mbps := 0.0
	if dur > 0 {
		mbps = float64(written) / dur.Seconds() / (1024 * 1024)
	}
	log.Info().
		Str("version", meta.VersionLabel).
		Int64("bytes", written).
		Dur("took", dur).
		Float64("mbps", mbps).
		Msg("AI bundle installed")
	return nil
}

func (u *Installer) localPath() string {
	return filepath.Join(u.aiRoot, "ai-client.exe")
}

// chunkCount is how many parallel HTTP Range requests we run.
// 4 hits the sweet spot for most consumer connections.
const chunkCount = 4

// minChunkedSize is the threshold below which we don't bother
// chunking — the chunking overhead outweighs the parallelism win
// for tiny payloads.
const minChunkedSize = 2 * 1024 * 1024

// downloadFile pulls a binary into tmp using parallel Range
// requests when the size justifies it, falling back to a single
// stream otherwise. Returns the byte count written.
func (u *Installer) downloadFile(ctx context.Context, url string, expectedSize int64, tmp string) (int64, error) {
	if expectedSize >= minChunkedSize {
		n, err := u.downloadChunked(ctx, url, expectedSize, tmp)
		if err == nil {
			return n, nil
		}
		if !errors.Is(err, api.ErrRangeNotSupported) {
			log.Warn().Err(err).Msg("chunked download failed, falling back to single stream")
		}
	}
	return u.downloadStreaming(ctx, url, expectedSize, tmp)
}

// downloadChunked opens chunkCount parallel HTTP Range requests and
// each goroutine writes its byte range via WriteAt. Defeats single-
// connection TCP slow-start on high-RTT links.
func (u *Installer) downloadChunked(ctx context.Context, url string, size int64, tmp string) (int64, error) {
	f, err := os.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o700)
	if err != nil {
		return 0, fmt.Errorf("create tmp: %w", err)
	}
	if err := f.Truncate(size); err != nil {
		_ = f.Close()
		return 0, fmt.Errorf("preallocate: %w", err)
	}

	chunk := size / chunkCount
	var (
		wg       sync.WaitGroup
		firstErr error
		errMu    sync.Mutex
	)
	setErr := func(e error) {
		errMu.Lock()
		if firstErr == nil {
			firstErr = e
		}
		errMu.Unlock()
	}

	for i := 0; i < chunkCount; i++ {
		start := int64(i) * chunk
		end := start + chunk - 1
		if i == chunkCount-1 {
			end = size - 1
		}
		wg.Add(1)
		go func(start, end int64) {
			defer wg.Done()
			if err := u.fetchRangeIntoFile(ctx, url, start, end, f); err != nil {
				setErr(err)
			}
		}(start, end)
	}
	wg.Wait()

	if err := f.Sync(); err != nil {
		_ = f.Close()
		return 0, fmt.Errorf("fsync chunked: %w", err)
	}
	if err := f.Close(); err != nil {
		return 0, err
	}
	if firstErr != nil {
		return 0, firstErr
	}
	return size, nil
}

func (u *Installer) fetchRangeIntoFile(ctx context.Context, url string, start, end int64, f *os.File) error {
	body, err := u.client.DownloadRange(ctx, url, start, end)
	if err != nil {
		return err
	}
	defer body.Close()

	buf := make([]byte, 64*1024)
	off := start
	for {
		n, rerr := body.Read(buf)
		if n > 0 {
			if _, werr := f.WriteAt(buf[:n], off); werr != nil {
				return fmt.Errorf("write at %d: %w", off, werr)
			}
			off += int64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fmt.Errorf("read range %d-%d: %w", start, end, rerr)
		}
	}
	return nil
}

// downloadStreaming is the single-connection fallback path used
// when the origin doesn't honour Range requests or the chunked path
// errors out for any other reason.
func (u *Installer) downloadStreaming(ctx context.Context, url string, expectedSize int64, tmp string) (int64, error) {
	body, contentLen, err := u.client.Download(ctx, url)
	if err != nil {
		return 0, err
	}
	defer body.Close()

	if expectedSize > 0 && contentLen > 0 && contentLen != expectedSize {
		return 0, fmt.Errorf("size mismatch: metadata says %d, server sent %d", expectedSize, contentLen)
	}

	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
	if err != nil {
		return 0, fmt.Errorf("create tmp: %w", err)
	}
	bufOut := bufio.NewWriterSize(out, 256*1024)
	written, err := io.Copy(bufOut, body)
	if err != nil {
		_ = out.Close()
		return 0, fmt.Errorf("copy: %w", err)
	}
	if err := bufOut.Flush(); err != nil {
		_ = out.Close()
		return 0, fmt.Errorf("flush: %w", err)
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return 0, fmt.Errorf("fsync: %w", err)
	}
	if err := out.Close(); err != nil {
		return 0, err
	}
	return written, nil
}

// hashFile returns the SHA256 hex of the file at path. Missing file
// returns empty string + nil so the caller can treat it the same as
// "stale".
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	br := bufio.NewReaderSize(f, 256*1024)
	if _, err := io.Copy(h, br); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ensureFreeSpace returns an error if the volume hosting `dir` has
// less than `need` bytes free.
func ensureFreeSpace(dir string, need int64) error {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	proc := kernel32.NewProc("GetDiskFreeSpaceExW")
	pathPtr, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return fmt.Errorf("disk space probe: %w", err)
	}
	var freeAvailable, totalBytes, totalFree uint64
	r1, _, callErr := proc.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		uintptr(unsafe.Pointer(&freeAvailable)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFree)),
	)
	if r1 == 0 {
		// Best-effort: probe failure shouldn't block the install.
		log.Debug().Err(callErr).Msg("disk free probe failed; continuing")
		return nil
	}
	if int64(freeAvailable) < need {
		return fmt.Errorf("not enough disk space: need %d bytes, have %d", need, freeAvailable)
	}
	return nil
}

func trim(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	if s == "" {
		return "(none)"
	}
	return s
}
