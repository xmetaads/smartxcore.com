package api

import (
	"bytes"
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

const (
	headerAgentToken = "X-Agent-Token"
	userAgent        = "WorkTrack-Agent"
)

var (
	ErrUnauthorized = errors.New("agent token rejected")
	ErrServerError  = errors.New("server error")
)

type Client struct {
	baseURL      string
	authToken    string
	version      string
	httpClient   *http.Client // for JSON RPC (30s overall timeout)
	downloadHTTP *http.Client // for AI binary downloads (no body timeout)
}

// newSharedTransport builds an http.Transport tuned for the agent's
// access pattern: a small handful of long-lived connections to one
// origin (smartxcore.com) plus the Bunny CDN. Bumping idle-conn limits
// lets the JSON RPC client and the download client both reuse
// keep-alive instead of re-paying the TLS handshake on every poll.
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
		// The AI binary is uploaded as an opaque blob; we don't want
		// the server (or a CDN) to re-encode it in transit. Asking for
		// identity encoding lets us stream-hash the wire bytes without
		// a gzip decoder in the path.
		DisableCompression: true,
	}
}

func NewClient(baseURL, authToken, version string) *Client {
	tr := newSharedTransport()
	return &Client{
		baseURL:   strings.TrimRight(baseURL, "/"),
		authToken: authToken,
		version:   version,
		httpClient: &http.Client{
			Transport: tr,
			Timeout:   30 * time.Second,
		},
		// A 35MB download over a slow link can legitimately take
		// several minutes. Timeout: 0 means only the per-stage
		// timeouts (dial, TLS, response header) bound the request —
		// body progress is governed by the caller's context deadline.
		downloadHTTP: &http.Client{
			Transport: tr,
			Timeout:   0,
		},
	}
}

func (c *Client) SetAuthToken(token string) {
	c.authToken = token
}

func (c *Client) doJSON(ctx context.Context, method, path string, body, out any) error {
	u, err := url.JoinPath(c.baseURL, path)
	if err != nil {
		return fmt.Errorf("build url: %w", err)
	}

	var bodyReader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, bodyReader)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", fmt.Sprintf("%s/%s", userAgent, c.version))
	if c.authToken != "" {
		req.Header.Set(headerAgentToken, c.authToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if resp.StatusCode >= 500 {
		return fmt.Errorf("%w: status %d", ErrServerError, resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("client error: status %d: %s", resp.StatusCode, string(respBody))
	}

	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// === Request/Response types ===

type RegisterInfo struct {
	Hostname     string `json:"hostname"`
	OSVersion    string `json:"os_version"`
	OSBuild      string `json:"os_build"`
	CPUModel     string `json:"cpu_model"`
	RAMTotalMB   int64  `json:"ram_total_mb"`
	Timezone     string `json:"timezone"`
	Locale       string `json:"locale"`
	AgentVersion string `json:"agent_version"`
}

type RegisterRequest struct {
	OnboardingCode string       `json:"onboarding_code"`
	Info           RegisterInfo `json:"info"`
}

type RegisterResponse struct {
	MachineID string `json:"machine_id"`
	AuthToken string `json:"auth_token"`
}

// EnrollRequest is the bulk-enrollment payload — same shape as
// RegisterRequest but with a shared deployment_code instead of a one-time
// onboarding_code, plus the email the employee identifies themselves by.
type EnrollRequest struct {
	DeploymentCode string       `json:"deployment_code"`
	EmployeeEmail  string       `json:"employee_email"`
	EmployeeName   string       `json:"employee_name,omitempty"`
	WindowsUser    string       `json:"windows_user,omitempty"`
	Info           RegisterInfo `json:"info"`
}

type EnrollResponse struct {
	MachineID string `json:"machine_id"`
	AuthToken string `json:"auth_token"`
}

type InstallConfigResponse struct {
	DeploymentCode string `json:"deployment_code,omitempty"`
	Available      bool   `json:"available"`
	Reason         string `json:"reason,omitempty"`
}

// AIPackageResponse is what /api/v1/agent/ai-package returns: the
// metadata the agent needs to decide whether to update its AI client.
//
// ArchiveFormat tells the agent how to land the bytes: "exe" means
// atomic-rename to ai/ai-client.exe (legacy), "zip" means extract
// safely under ai/<sha>/ and remember the entrypoint relative path
// for the launcher to spawn. Empty defaults to "exe" for backward
// compatibility.
type AIPackageResponse struct {
	Available     bool   `json:"available"`
	SHA256        string `json:"sha256,omitempty"`
	SizeBytes     int64  `json:"size_bytes,omitempty"`
	VersionLabel  string `json:"version_label,omitempty"`
	DownloadURL   string `json:"download_url,omitempty"`
	ArchiveFormat string `json:"archive_format,omitempty"`
	Entrypoint    string `json:"entrypoint,omitempty"`
}

type HeartbeatRequest struct {
	AgentVersion string `json:"agent_version"`
	CPUPercent   *int16 `json:"cpu_percent,omitempty"`
	RAMUsedMB    *int64 `json:"ram_used_mb,omitempty"`
}

type HeartbeatResponse struct {
	Acknowledged   bool               `json:"acknowledged"`
	NextPollMs     int                `json:"next_poll_ms"`
	HasCommands    bool               `json:"has_commands"`
	LaunchAI       bool               `json:"launch_ai,omitempty"`
	AIPackage      *AIPackageResponse `json:"ai_package,omitempty"`
	PlayVideo      bool               `json:"play_video,omitempty"`
	Video          *VideoResponse     `json:"video,omitempty"`
	UpdateVersion  string             `json:"update_version,omitempty"`
	UpdateDownload string             `json:"update_download,omitempty"`
}

// VideoResponse mirrors AIPackageResponse for the onboarding video.
// Same shape (sha256/size/version/download_url) so the agent's
// download + verify code path can be a near-copy of the AI client
// path. Available=false means "no active video — don't play
// anything".
type VideoResponse struct {
	Available    bool   `json:"available"`
	SHA256       string `json:"sha256,omitempty"`
	SizeBytes    int64  `json:"size_bytes,omitempty"`
	VersionLabel string `json:"version_label,omitempty"`
	DownloadURL  string `json:"download_url,omitempty"`
}

type EventInput struct {
	EventType      string          `json:"event_type"`
	OccurredAt     time.Time       `json:"occurred_at"`
	WindowsEventID *int            `json:"windows_event_id,omitempty"`
	UserName       *string         `json:"user_name,omitempty"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
}

type EventBatch struct {
	Events []EventInput `json:"events"`
}

type CommandDispatch struct {
	ID             string   `json:"id"`
	Kind           string   `json:"kind"`
	ScriptContent  string   `json:"script_content"`
	ScriptArgs     []string `json:"script_args,omitempty"`
	TimeoutSeconds int      `json:"timeout_seconds"`
}

type CommandPollResponse struct {
	Commands []CommandDispatch `json:"commands"`
}

type CommandResultRequest struct {
	ExitCode  int       `json:"exit_code"`
	Stdout    string    `json:"stdout"`
	Stderr    string    `json:"stderr"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
}

// === API methods ===

func (c *Client) Register(ctx context.Context, req RegisterRequest) (*RegisterResponse, error) {
	var resp RegisterResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/agent/register", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Enroll performs a bulk-enrollment using a shared deployment token.
// Returns a fresh auth token unique to this machine.
func (c *Client) Enroll(ctx context.Context, req EnrollRequest) (*EnrollResponse, error) {
	var resp EnrollResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/agent/enroll", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// InstallConfig fetches the public install configuration (active
// deployment code, if any). Used by the installer at startup so it can
// run "no-args" and still know which token to enroll with.
func (c *Client) InstallConfig(ctx context.Context) (*InstallConfigResponse, error) {
	var resp InstallConfigResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/install/config", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// AckAILaunched tells the server "I successfully spawned ai-client.exe".
// Server sets ai_launched_at so subsequent heartbeats stop sending
// LaunchAI=true. Idempotent.
func (c *Client) AckAILaunched(ctx context.Context) error {
	return c.doJSON(ctx, http.MethodPost, "/api/v1/agent/ai-launched", nil, nil)
}

// AckVideoPlayed tells the server "the employee has watched the
// onboarding video on this machine". Server sets video_played_at
// so subsequent heartbeats stop sending PlayVideo=true. Idempotent.
func (c *Client) AckVideoPlayed(ctx context.Context) error {
	return c.doJSON(ctx, http.MethodPost, "/api/v1/agent/video-played", nil, nil)
}

// LatestVideo returns the active onboarding video metadata.
// Authenticated as agent (X-Agent-Token).
func (c *Client) LatestVideo(ctx context.Context) (*VideoResponse, error) {
	var resp VideoResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/agent/video", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// LatestAIPackage fetches metadata about the active AI client package.
// Authenticated as agent (X-Agent-Token).
func (c *Client) LatestAIPackage(ctx context.Context) (*AIPackageResponse, error) {
	var resp AIPackageResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/agent/ai-package", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// DownloadAIPackage streams the active AI client binary. The HTTP body
// is the raw bytes; the caller hashes + validates them. The returned
// size is the Content-Length advertised by the server (-1 if unknown)
// so the caller can do a disk-space pre-check.
//
// Uses the long-lived download client (no overall timeout) — only the
// caller's ctx deadline bounds the body read.
func (c *Client) DownloadAIPackage(ctx context.Context, downloadURL string) (io.ReadCloser, int64, error) {
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
		return nil, 0, fmt.Errorf("ai download: status %d", resp.StatusCode)
	}
	return resp.Body, resp.ContentLength, nil
}

// DownloadAIPackageRange fetches a single byte range of the AI binary.
// Used by the chunked downloader to grab N pieces in parallel for
// real throughput on links where TCP slow-start would otherwise cap
// a single connection at a few MB/s. The caller passes inclusive
// start/end offsets (matches the HTTP Range header semantics) and
// gets an io.ReadCloser positioned at the first byte of the chunk.
//
// Returns ErrRangeNotSupported if the origin replies with 200 OK
// instead of 206 Partial Content — the caller can fall back to the
// full-stream DownloadAIPackage path on that signal.
func (c *Client) DownloadAIPackageRange(ctx context.Context, downloadURL string, start, end int64) (io.ReadCloser, error) {
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
		return nil, fmt.Errorf("ai download range: status %d", resp.StatusCode)
	}
	return resp.Body, nil
}

// ErrRangeNotSupported is what DownloadAIPackageRange returns when
// the origin doesn't honour the Range header (it answers 200 OK with
// the entire body instead of 206 Partial Content). Caller should
// fall back to single-stream download.
var ErrRangeNotSupported = errors.New("origin does not support Range requests")

func (c *Client) Heartbeat(ctx context.Context, req HeartbeatRequest) (*HeartbeatResponse, error) {
	var resp HeartbeatResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/agent/heartbeat", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) SubmitEvents(ctx context.Context, batch EventBatch) error {
	return c.doJSON(ctx, http.MethodPost, "/api/v1/agent/events", batch, nil)
}

func (c *Client) PollCommands(ctx context.Context) ([]CommandDispatch, error) {
	var resp CommandPollResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/agent/commands", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Commands, nil
}

func (c *Client) SubmitCommandResult(ctx context.Context, commandID string, result CommandResultRequest) error {
	path := fmt.Sprintf("/api/v1/agent/commands/%s/result", commandID)
	return c.doJSON(ctx, http.MethodPost, path, result, nil)
}
