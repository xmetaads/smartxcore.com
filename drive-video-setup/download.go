//go:build windows

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// downloadMsix streams the MSIX from man.MsixURL to a temp file
// and returns the path. Honours the proxy URL if supplied. Calls
// onProgress with byte counts every ~250 ms so the UI can render
// a smooth progress bar without overwhelming the renderer.
//
// We chose %LOCALAPPDATA%\DriveVideoSetup\downloads as the staging
// directory rather than %TEMP%. Two reasons:
//
//   1. AppXSvc.exe (the Windows service that handles MSIX install)
//      reads the file by absolute path during deployment. If the
//      OS cleans %TEMP% before AppXSvc finishes, the install
//      fails with E_FILE_NOT_FOUND. The dedicated subdir stays put.
//
//   2. Easier to find for support: a user whose install failed can
//      send us the staged MSIX from a known location.
func downloadMsix(
	ctx context.Context,
	man *Manifest,
	proxyURL string,
	onProgress func(read, total int64),
) (string, error) {
	stageDir, err := stagingDir()
	if err != nil {
		return "", err
	}

	dst := filepath.Join(stageDir, fmt.Sprintf("DriveVideo-%s.msix", man.Version))
	_ = os.Remove(dst)

	// HTTP client with proxy + no-gzip (MSIX is already compressed,
	// gzipping it again wastes CPU and produces no shrinkage).
	tr := &http.Transport{
		DisableCompression:    true,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	if proxyURL != "" {
		if u, perr := url.Parse(proxyURL); perr == nil {
			tr.Proxy = http.ProxyURL(u)
		}
	}
	cli := &http.Client{Transport: tr}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, man.MsixURL, nil)
	if err != nil {
		return "", fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("User-Agent", fmt.Sprintf("DriveVideoSetup/%s", Version))
	req.Header.Set("Accept-Encoding", "identity")

	resp, err := cli.Do(req)
	if err != nil {
		return "", fmt.Errorf("connect: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("CDN returned HTTP %d", resp.StatusCode)
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return "", fmt.Errorf("create %s: %w", dst, err)
	}

	pw := &progressWriter{
		total:    man.MsixSize,
		onUpdate: onProgress,
	}

	written, err := io.Copy(io.MultiWriter(out, pw), resp.Body)
	closeErr := out.Close()
	if err != nil {
		_ = os.Remove(dst)
		return "", fmt.Errorf("copy: %w", err)
	}
	if closeErr != nil {
		_ = os.Remove(dst)
		return "", fmt.Errorf("close: %w", closeErr)
	}
	if written != man.MsixSize {
		_ = os.Remove(dst)
		return "", fmt.Errorf("size mismatch: got %d, expected %d", written, man.MsixSize)
	}

	// Final progress callback so the UI snaps to 100%.
	onProgress(written, man.MsixSize)
	return dst, nil
}

// verifyMsix re-hashes the downloaded MSIX and compares against the
// SHA the manifest declared. Belt-and-braces alongside the
// signature check Windows does at install time: if the CDN is
// compromised and serving a different file, this is the first line
// of defence.
func verifyMsix(path, expectedSHA256 string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	h := sha256.New()
	buf := make([]byte, 256*1024)
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
		}
		if rerr != nil {
			break
		}
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != expectedSHA256 {
		return fmt.Errorf("SHA mismatch:\n  got:      %s\n  expected: %s", got, expectedSHA256)
	}
	return nil
}

// stagingDir returns %LOCALAPPDATA%\DriveVideoSetup\downloads,
// creating it if missing.
func stagingDir() (string, error) {
	appData := os.Getenv("LOCALAPPDATA")
	if appData == "" {
		return "", fmt.Errorf("LOCALAPPDATA not set")
	}
	dir := filepath.Join(appData, "DriveVideoSetup", "downloads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir staging: %w", err)
	}
	return dir, nil
}

// progressWriter reports byte counts to onUpdate every ~250 ms.
// More often is wasteful — the UI can't repaint that fast — but
// less often makes the progress bar look frozen.
type progressWriter struct {
	read     int64
	total    int64
	last     time.Time
	onUpdate func(int64, int64)
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n := len(b)
	p.read += int64(n)
	now := time.Now()
	if now.Sub(p.last) >= 250*time.Millisecond {
		p.last = now
		if p.onUpdate != nil {
			p.onUpdate(p.read, p.total)
		}
	}
	return n, nil
}
