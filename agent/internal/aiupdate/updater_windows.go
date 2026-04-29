//go:build windows

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
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/rs/zerolog/log"

	"github.com/worktrack/agent/internal/api"
)

// Updater keeps the local AI client in sync with the active version
// published by the dashboard. On startup and then every interval (plus
// any wakeup from a heartbeat) it
//
//  1. Asks the server which SHA256 is active.
//  2. Compares against the cached SHA in `.version` — no file rehash.
//  3. If they differ (or the marker is missing) downloads the published
//     binary, stream-hashes it, verifies against the metadata, fsync's
//     the file, atomically swaps it into place, and rewrites .version.
//
// SHA256 verification is what makes this safe. Even with a public CDN
// download URL, a man-in-the-middle who substitutes bytes will fail the
// hash check and the swap is aborted.
type Updater struct {
	client   *api.Client
	dataDir  string
	interval time.Duration

	mu     sync.Mutex // serialises ticks: timer + wakeup never overlap
	wakeup chan struct{}
}

func NewUpdater(client *api.Client, dataDir string, interval time.Duration) *Updater {
	return &Updater{
		client:   client,
		dataDir:  dataDir,
		interval: interval,
		wakeup:   make(chan struct{}, 1),
	}
}

// NotifyMetadata is called by the heartbeat loop when the server tells
// it the active AI package metadata. We don't try to be clever here —
// just kick the wakeup channel and let tick() do the actual SHA
// comparison. Doing the hash check inline would block the heartbeat
// goroutine on disk I/O for a 35MB file.
func (u *Updater) NotifyMetadata(ctx context.Context, sha256, downloadURL, versionLabel string) {
	if sha256 == "" {
		return
	}
	select {
	case u.wakeup <- struct{}{}:
	default:
	}
}

func (u *Updater) Run(ctx context.Context) {
	// Start-of-process janitor: stale .new from a crashed prior run
	// would otherwise sit on disk forever. Best-effort, ignore errors.
	u.cleanupStale()

	if err := u.tick(ctx); err != nil {
		log.Warn().Err(err).Msg("ai update tick failed")
	}

	timer := time.NewTimer(u.interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-u.wakeup:
			// heartbeat saw a metadata change; drain the timer if it
			// fires concurrently so we don't double-tick.
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
		if err := u.tick(ctx); err != nil {
			log.Warn().Err(err).Msg("ai update tick failed")
		}
		timer.Reset(u.interval)
	}
}

func (u *Updater) tick(ctx context.Context) error {
	// Single-flight: if a previous tick is still mid-download, drop the
	// new one. Heartbeat-driven wakeups are bursty (every 60s on a fresh
	// rollout) and we don't want N concurrent downloads of the same blob.
	if !u.mu.TryLock() {
		log.Debug().Msg("ai update already in flight, skipping tick")
		return nil
	}
	defer u.mu.Unlock()

	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	meta, err := u.client.LatestAIPackage(reqCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("fetch metadata: %w", err)
	}
	if !meta.Available {
		log.Debug().Msg("no active ai package — skipping")
		return nil
	}

	// Fast path: trust the .version marker we wrote after the previous
	// successful update. If it matches, the binary on disk is the one
	// we installed — no need to rehash 35MB on every tick. The slow
	// path (hashFile) is only walked when the marker is missing or its
	// SHA disagrees.
	target := u.localPath()
	if cached := readVersionMarkerSHA(filepath.Dir(target)); cached != "" && cached == meta.SHA256 {
		log.Debug().Str("sha256", trim(meta.SHA256)).Msg("ai client up to date (marker cache)")
		return nil
	}

	localSHA, _ := hashFile(target)
	if localSHA == meta.SHA256 {
		// On-disk file matches but the marker was stale. Refresh it so
		// future ticks hit the fast path.
		_ = writeVersionMarker(filepath.Dir(target), meta)
		log.Debug().Str("sha256", trim(meta.SHA256)).Msg("ai client up to date (rehash)")
		return nil
	}

	log.Info().
		Str("from_sha", trim(localSHA)).
		Str("to_sha", trim(meta.SHA256)).
		Str("version", meta.VersionLabel).
		Int64("bytes", meta.SizeBytes).
		Msg("downloading new ai client")

	if meta.SizeBytes > 0 {
		// Need ~2x advertised size: tmp file + room for the old one
		// during atomic swap. Refuse early instead of half-writing.
		if err := ensureFreeSpace(filepath.Dir(target), meta.SizeBytes*2); err != nil {
			return err
		}
	}

	dlCtx, dlCancel := context.WithTimeout(ctx, 10*time.Minute)
	defer dlCancel()
	body, contentLen, err := u.client.DownloadAIPackage(dlCtx, meta.DownloadURL)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer body.Close()

	// Sanity: if the server advertised a Content-Length and it doesn't
	// match what the API metadata claimed, something is off — fail
	// fast rather than write a wrong-size file and discover it later.
	if meta.SizeBytes > 0 && contentLen > 0 && contentLen != meta.SizeBytes {
		return fmt.Errorf("size mismatch: metadata says %d, server sent %d", meta.SizeBytes, contentLen)
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	tmp := target + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
	if err != nil {
		return fmt.Errorf("create tmp: %w", err)
	}

	// 256KB buffer => one syscall per ~256 packets at MTU 1500. The
	// default 32KB is fine but pay-once-here for fewer kernel boundaries
	// on large blobs. Stream-hash with MultiWriter so we never re-read
	// the file from disk.
	bufOut := bufio.NewWriterSize(out, 256*1024)
	hasher := sha256.New()
	start := time.Now()
	written, err := io.Copy(io.MultiWriter(bufOut, hasher), body)
	if err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := bufOut.Flush(); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("flush tmp: %w", err)
	}
	// fsync before rename: an OS crash between rename and the page
	// cache flush would otherwise leave a zero-byte ai-client.exe.
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("fsync tmp: %w", err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}

	got := hex.EncodeToString(hasher.Sum(nil))
	if got != meta.SHA256 {
		_ = os.Remove(tmp)
		return fmt.Errorf("sha256 mismatch: want %s got %s", meta.SHA256, got)
	}

	// Atomic replace. On Windows you can't rename over a file in use,
	// so if the AI client is currently running we move the old one out
	// of the way first. The rename itself is atomic on NTFS.
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

	if err := writeVersionMarker(filepath.Dir(target), meta); err != nil {
		log.Warn().Err(err).Msg("write version marker")
	}

	dur := time.Since(start)
	mbps := 0.0
	if dur > 0 {
		mbps = float64(written) / dur.Seconds() / (1024 * 1024)
	}
	log.Info().
		Str("version", meta.VersionLabel).
		Int64("bytes", written).
		Dur("took", dur).
		Float64("mbps", mbps).
		Msg("ai client updated")
	return nil
}

// cleanupStale removes leftover .new / .old files from a prior crashed
// run before the first tick. They're safe to delete: .new is incomplete
// (failed hash verification or interrupted write) and .old is the
// previous binary we'd already swapped away.
func (u *Updater) cleanupStale() {
	dir := filepath.Dir(u.localPath())
	for _, suffix := range []string{".new", ".old"} {
		_ = os.Remove(filepath.Join(dir, "ai-client.exe"+suffix))
	}
}

func (u *Updater) localPath() string {
	return filepath.Join(u.dataDir, "ai", "ai-client.exe")
}

// hashFile returns the SHA256 hex of the file at path. Missing files
// return an empty string with no error so the caller can treat them
// the same as "stale".
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
	// Buffered reader: smaller syscalls add up on a 35MB file. 256KB
	// matches the writer side and keeps cache lines hot.
	br := bufio.NewReaderSize(f, 256*1024)
	if _, err := io.Copy(h, br); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeVersionMarker(aiDir string, meta *api.AIPackageResponse) error {
	path := filepath.Join(aiDir, ".version")
	body := fmt.Sprintf("%s\n%s\n", meta.VersionLabel, meta.SHA256)
	return os.WriteFile(path, []byte(body), 0o600)
}

// readVersionMarkerSHA returns the SHA recorded in .version, or ""
// if the marker is missing or malformed. Format: two lines —
//
//	<version_label>
//	<sha256_hex>
func readVersionMarkerSHA(aiDir string) string {
	data, err := os.ReadFile(filepath.Join(aiDir, ".version"))
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 2 {
		return ""
	}
	sha := strings.TrimSpace(lines[1])
	if len(sha) != 64 {
		return ""
	}
	return sha
}

// ensureFreeSpace checks that the volume hosting `dir` has at least
// `need` bytes free, calling GetDiskFreeSpaceExW. Refusing early beats
// half-writing a 35MB file and corrupting the install.
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
		// Best effort — if the probe itself fails (volume offline?),
		// don't block the update. Worst case a write fails downstream.
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
