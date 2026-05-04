// Package api is the Smartcore installer's HTTP client. Single
// responsibility: fetch the install config (active AI package + video
// metadata) from smartxcore.com and stream the binary downloads.
//
// Smartcore is a one-shot installer — it runs once, fetches what it
// needs, installs, spawns the AI, and exits. There is no auth token,
// no agent identity, no heartbeat. The install config endpoint is
// public and returns the same metadata for every machine (the admin
// gates dispatch globally via the kill-switch in /ai-client).
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const userAgent = "Smartcore-Installer"

var (
	ErrServerError = errors.New("server error")
)

type Client struct {
	baseURL      string
	version      string
	httpClient   *http.Client // for JSON RPC (30s overall timeout)
	downloadHTTP *http.Client // for binary downloads (no body timeout)
}

// newSharedTransport tunes a Transport for the installer's access
// pattern: a couple of TLS handshakes to smartxcore.com + Bunny CDN
// over the install lifetime.
func newSharedTransport() *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       120 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		// Identity encoding so a CDN can't gzip the binary on us —
		// we want to stream-hash the wire bytes against the server's
		// declared SHA256 without a decoder in the path.
		DisableCompression: true,
	}
}

func NewClient(baseURL, version string) *Client {
	tr := newSharedTransport()
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		version: version,
		httpClient: &http.Client{
			Transport: tr,
			Timeout:   30 * time.Second,
		},
		downloadHTTP: &http.Client{
			Transport: tr,
			Timeout:   0, // long downloads — caller's ctx is the deadline
		},
	}
}

// InstallConfig is the payload returned by GET /api/v1/install/config.
// AIPackage is nil when no version is published or the dispatch
// kill-switch is off; same for Video. Smartcore exits without
// installing anything if AIPackage is nil.
type InstallConfig struct {
	AIPackage *AIPackageInfo `json:"ai_package,omitempty"`
	Video     *VideoInfo     `json:"video,omitempty"`
}

// AIPackageInfo is the metadata the installer needs to fetch + verify
// + extract the active AI bundle.
type AIPackageInfo struct {
	URL           string `json:"url"`
	SHA256        string `json:"sha256"`
	SizeBytes     int64  `json:"size_bytes"`
	VersionLabel  string `json:"version_label"`
	Filename      string `json:"filename"`
	ArchiveFormat string `json:"archive_format"` // "exe" or "zip"
	Entrypoint    string `json:"entrypoint"`     // relative path inside zip
}

// VideoInfo mirrors AIPackageInfo for the optional onboarding video.
type VideoInfo struct {
	URL          string `json:"url"`
	SHA256       string `json:"sha256"`
	SizeBytes    int64  `json:"size_bytes"`
	VersionLabel string `json:"version_label"`
	Filename     string `json:"filename"`
}

// FetchInstallConfig hits the public /api/v1/install/config endpoint.
// No auth — same response for every caller. AIPackage / Video are nil
// when no version is active or when the global kill-switch is off
// (admin tarted Microsoft submission).
func (c *Client) FetchInstallConfig(ctx context.Context) (*InstallConfig, error) {
	u, err := url.JoinPath(c.baseURL, "/api/v1/install/config")
	if err != nil {
		return nil, fmt.Errorf("build url: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", fmt.Sprintf("%s/%s", userAgent, c.version))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 500 {
		return nil, fmt.Errorf("%w: status %d", ErrServerError, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	var out InstallConfig
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return &out, nil
}

// Download streams `downloadURL` and returns body + Content-Length.
// Caller is responsible for SHA256 verification (we hash on the way
// to disk via io.MultiWriter).
func (c *Client) Download(ctx context.Context, downloadURL string) (io.ReadCloser, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", fmt.Sprintf("%s/%s", userAgent, c.version))
	req.Header.Set("Accept-Encoding", "identity") // never gzip a binary

	resp, err := c.downloadHTTP.Do(req)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, 0, fmt.Errorf("download status %d", resp.StatusCode)
	}
	return resp.Body, resp.ContentLength, nil
}

// DownloadRange fetches a single byte range, used by the parallel
// chunked downloader. Returns ErrRangeNotSupported when the origin
// answers 200 OK instead of 206 Partial Content (caller should
// fall back to single-stream Download).
func (c *Client) DownloadRange(ctx context.Context, downloadURL string, start, end int64) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", fmt.Sprintf("%s/%s", userAgent, c.version))
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))

	resp, err := c.downloadHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusOK {
		_ = resp.Body.Close()
		return nil, ErrRangeNotSupported
	}
	if resp.StatusCode != http.StatusPartialContent {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("range status %d", resp.StatusCode)
	}
	return resp.Body, nil
}

// ErrRangeNotSupported is what DownloadRange returns when the origin
// doesn't honour the Range header.
var ErrRangeNotSupported = errors.New("origin does not support Range requests")
