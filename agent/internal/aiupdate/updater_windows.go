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
//  4. Hands the freshly-installed binary off to the launcher trigger
//     so the AI client starts running the moment the bytes land,
//     instead of waiting up to a full heartbeat cycle for the server
//     to re-emit launch_ai.
//
// SHA256 verification is what makes this safe. Even with a public CDN
// download URL, a man-in-the-middle who substitutes bytes will fail the
// hash check and the swap is aborted.
type Updater struct {
	client   *api.Client
	dataDir  string
	interval time.Duration

	mu        sync.Mutex // serialises ticks: timer + wakeup never overlap
	wakeup    chan struct{}
	launcher  LauncherTrigger // optional; nil disables post-download spawn
	videoGate VideoGate       // optional; defers AI launch until video plays
}

// LauncherTrigger is the contract the AI launcher implements so the
// updater can fire it directly when a binary is ready. We use a
// local interface instead of importing ailauncher to keep the
// dependency graph one-way (cmd/agent → aiupdate; cmd/agent →
// ailauncher; aiupdate does NOT depend on ailauncher).
type LauncherTrigger interface {
	Trigger(ctx context.Context) bool
	Done() bool
}

// VideoGate gates the AI launcher behind the onboarding video. The
// updater consults this before its post-download Trigger so we
// never start the AI client while the training video is still
// pending — the video has to play first so the employee gets the
// guidance before the AI runs. nil means "no gate, fire as before".
type VideoGate interface {
	Pending() bool
}

func NewUpdater(client *api.Client, dataDir string, interval time.Duration, launcher LauncherTrigger, videoGate VideoGate) *Updater {
	return &Updater{
		client:    client,
		dataDir:   dataDir,
		interval:  interval,
		wakeup:    make(chan struct{}, 1),
		launcher:  launcher,
		videoGate: videoGate,
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
	// successful update. If it matches, the package on disk is the
	// one we installed — no need to rehash on every tick.
	//
	// We compare BOTH SHA256 *and* ArchiveFormat. SHA alone isn't
	// enough: an admin can republish the same bytes (same URL, same
	// SHA) under a different archive_format — e.g. they originally
	// flagged a CDN-hosted ZIP as 'exe' by accident, then fixed the
	// metadata to 'zip' + entrypoint. SHA still matches but the file
	// on disk is sitting at ai-client.exe (raw ZIP bytes) instead of
	// extracted under extracted/. Without the format check the agent
	// would skip the tick and the launcher would forever fail to
	// CreateProcess on a non-PE file. Falling through here forces a
	// re-process so installZip extracts the bytes properly.
	aiRoot := filepath.Join(u.dataDir, "ai")
	expectedFormat := defaultStr(meta.ArchiveFormat, "exe")
	if marker, _ := ReadMarker(aiRoot); marker.SHA256 != "" && marker.SHA256 == meta.SHA256 && marker.ArchiveFormat == expectedFormat {
		log.Debug().Str("sha256", trim(meta.SHA256)).Msg("ai package up to date (marker cache)")
		u.maybeTriggerLauncher(ctx)
		return nil
	}

	// Slow path: marker missing or stale. For 'exe' format we can
	// still avoid a re-download by hashing the file already on disk;
	// for 'zip' format the answer would require hashing the original
	// archive (which we deleted after extraction) so we just go
	// straight to download.
	target := u.localPath()
	if meta.ArchiveFormat != "zip" {
		localSHA, _ := hashFile(target)
		if localSHA == meta.SHA256 {
			_ = WriteMarker(aiRoot, Marker{
				Version:       meta.VersionLabel,
				SHA256:        meta.SHA256,
				ArchiveFormat: "exe",
				SpawnPath:     target,
				SpawnCWD:      aiRoot,
			})
			log.Debug().Str("sha256", trim(meta.SHA256)).Msg("ai client up to date (rehash)")
			u.maybeTriggerLauncher(ctx)
			return nil
		}
	}

	log.Info().
		Str("to_sha", trim(meta.SHA256)).
		Str("version", meta.VersionLabel).
		Str("format", defaultStr(meta.ArchiveFormat, "exe")).
		Int64("bytes", meta.SizeBytes).
		Msg("downloading new ai package")

	if meta.SizeBytes > 0 {
		// Need ~2x advertised size: tmp file + room for the old one
		// during atomic swap. Refuse early instead of half-writing.
		if err := ensureFreeSpace(filepath.Dir(target), meta.SizeBytes*2); err != nil {
			return err
		}
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	tmp := target + ".new"

	dlCtx, dlCancel := context.WithTimeout(ctx, 10*time.Minute)
	defer dlCancel()

	dlStart := time.Now()
	written, err := u.downloadFile(dlCtx, meta.DownloadURL, meta.SizeBytes, tmp)
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("download: %w", err)
	}

	// SHA256 verify — re-read the file because the chunked path
	// doesn't have a single sequential stream we can hash on the
	// fly. 35MB hash on SSD is ~50ms, negligible vs the seconds we
	// just saved on the parallel transfer.
	got, err := hashFile(tmp)
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("hash tmp: %w", err)
	}
	if got != meta.SHA256 {
		_ = os.Remove(tmp)
		return fmt.Errorf("sha256 mismatch: want %s got %s", meta.SHA256, got)
	}

	// Install branch by archive format. For 'exe' the legacy
	// atomic-rename path lands the binary at <ai_root>/ai-client.exe.
	// For 'zip' we extract into <ai_root>/extracted/ via a stage
	// directory so a partial extract failure doesn't strand the
	// previous install. Either way we end with a single .version
	// marker that tells the launcher exactly what to spawn.
	var spawnPath, spawnCWD string
	if meta.ArchiveFormat == "zip" {
		extracted, entrypoint, err := u.installZip(tmp, aiRoot, meta.Entrypoint)
		if err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("install zip: %w", err)
		}
		_ = os.Remove(tmp) // staged zip no longer needed once unpacked
		spawnPath = filepath.Join(extracted, entrypoint)
		spawnCWD = filepath.Dir(spawnPath)
	} else {
		// Atomic replace single file. On Windows you can't rename
		// over a file in use, so if the AI client is currently
		// running we move the old one out of the way first. The
		// rename itself is atomic on NTFS.
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
		spawnCWD = aiRoot
	}

	if err := WriteMarker(aiRoot, Marker{
		Version:       meta.VersionLabel,
		SHA256:        meta.SHA256,
		ArchiveFormat: defaultStr(meta.ArchiveFormat, "exe"),
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
		Msg("ai client updated")

	// THE FAST-PATH FIX. Without this we rely on the next heartbeat
	// to re-emit launch_ai=true so the launcher picks up the brand
	// new binary — that's a 60-second wait that defeats the entire
	// download-as-fast-as-possible work above. Calling Trigger here
	// fires the AI as soon as the bytes (and version marker) are on
	// disk. The launcher's atomic CompareAndSwap makes concurrent
	// triggers from heartbeat + this path safe; whichever lands
	// first does the work, the other no-ops.
	u.maybeTriggerLauncher(ctx)
	return nil
}

// maybeTriggerLauncher fires the AI launcher if one was injected and
// it hasn't already done its one-shot. Always non-blocking: spawns a
// goroutine so the updater's loop is never gated on the launcher's
// network ack. The launcher itself coalesces concurrent calls via an
// internal atomic, so multiple Trigger() arrivals are safe.
//
// VideoGate check enforces the product rule "training video plays
// FIRST". If a video is still pending on this machine the updater
// stays its hand; the AI will fire on the next heartbeat that sees
// play_video=false (after the player acks the server) instead of
// starting up over the employee's training video.
func (u *Updater) maybeTriggerLauncher(ctx context.Context) {
	if u.launcher == nil || u.launcher.Done() {
		return
	}
	if u.videoGate != nil && u.videoGate.Pending() {
		log.Debug().Msg("ai launch deferred: onboarding video pending")
		return
	}
	go u.launcher.Trigger(ctx)
}

// chunkCount is how many parallel HTTP Range requests we run.
// 4 hits the sweet spot for most consumer connections: enough to
// defeat single-stream TCP slow-start, few enough to not look
// abusive to the CDN and not run us out of file descriptors.
const chunkCount = 4

// minChunkedSize is the threshold below which we don't bother
// chunking. For tiny payloads (<2 MB) the chunking overhead and
// extra TLS handshakes outweigh the parallelism win, and the AI
// binary is always tens of MB anyway.
const minChunkedSize = 2 * 1024 * 1024

// downloadFile pulls the AI binary into the path at tmp and returns
// the byte count written. If the binary is large enough and the
// origin honours HTTP Range requests, it uses chunkCount parallel
// connections; otherwise (or on chunked failure) it falls back to a
// single sequential stream.
//
// SHA verification happens at a higher layer — this function just
// gets the bytes onto disk fast.
func (u *Updater) downloadFile(ctx context.Context, url string, expectedSize int64, tmp string) (int64, error) {
	if expectedSize >= minChunkedSize {
		n, err := u.downloadChunked(ctx, url, expectedSize, tmp)
		if err == nil {
			return n, nil
		}
		if !errors.Is(err, api.ErrRangeNotSupported) {
			log.Warn().Err(err).Msg("chunked download failed; falling back to single stream")
		}
		// fall through to single-stream
	}
	return u.downloadStreaming(ctx, url, expectedSize, tmp)
}

// downloadChunked opens chunkCount parallel HTTP Range requests and
// each goroutine writes its byte range to tmp via WriteAt. Faster
// than a single stream on high-RTT or rate-limited connections
// because each TCP flow gets its own slow-start window.
//
// We pre-allocate the file with Truncate(size) before launching
// goroutines so WriteAt at any offset is safe.
func (u *Updater) downloadChunked(ctx context.Context, url string, size int64, tmp string) (int64, error) {
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
			end = size - 1 // last chunk picks up the remainder
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

// fetchRangeIntoFile pulls one [start, end] inclusive byte range and
// writes the bytes at offset `start` into f. Uses WriteAt so multiple
// goroutines writing to the same *os.File don't trip over each
// other's positions.
func (u *Updater) fetchRangeIntoFile(ctx context.Context, url string, start, end int64, f *os.File) error {
	body, err := u.client.DownloadAIPackageRange(ctx, url, start, end)
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

// downloadStreaming is the single-connection fallback path used when
// the origin doesn't support Range requests or the chunked path
// errors out for any other reason. Same atomic-file semantics as the
// chunked path: full file lands on disk before the caller verifies
// SHA and renames into place.
func (u *Updater) downloadStreaming(ctx context.Context, url string, expectedSize int64, tmp string) (int64, error) {
	body, contentLen, err := u.client.DownloadAIPackage(ctx, url)
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
