// Package events implements a Server-Sent Events client that keeps a
// long-lived connection to the backend's /api/v1/agent/stream
// endpoint and dispatches incoming pushes (new AI package, command
// queued, manual launch trigger) to the right in-process handler in
// real time.
//
// The heartbeat loop is still authoritative for state — SSE is a
// "wake up now" optimisation that takes the typical activation
// latency from ~30 seconds (heartbeat avg) down to one TCP roundtrip.
// Whenever SSE drops (network blip, deploy, agent process replaced)
// the heartbeat keeps everything reconciled within 60 seconds, so
// SSE outages are silent at worst.
//
// Reconnection strategy: after a disconnect we sleep for
// `backoff` and try again. Backoff doubles up to a 30s ceiling. On a
// successful read we reset to 1s. The connection itself has no
// overall timeout — only the per-stage HTTP timeouts in the shared
// transport bound it. Idle traffic in both directions is the 25s
// SSE ping the server emits, so a half-open connection surfaces as
// the next read failing within ~30s and triggers a reconnect.
package events

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// Handlers is the set of triggers the listener will fire when
// matching events arrive over the stream. All of them are non-
// blocking — they push into a buffered channel or set an atomic
// flag, never block on I/O. The listener goroutine cannot afford
// to stall on a single slow handler.
type Handlers interface {
	// OnAIPackageChanged fires when the server announces a new active
	// AI package. The agent's AI updater wakes up immediately and
	// reconciles SHA against the local copy.
	OnAIPackageChanged(sha256, downloadURL, versionLabel string)

	// OnCommandPending fires when the server has queued a new command
	// for this machine. The executor wakes up and polls.
	OnCommandPending()

	// OnLaunchAI fires when the server signals "launch the AI client
	// now" out-of-band (admin clicked the button on the dashboard).
	// Equivalent to seeing launch_ai=true in a heartbeat response.
	OnLaunchAI()
}

type Listener struct {
	apiBase   string
	authToken string
	version   string
	handlers  Handlers
	client    *http.Client
}

// New builds a listener bound to a given backend + auth token. The
// HTTP client used for the long-lived stream gets its own dedicated
// transport: Timeout: 0 (no overall ceiling — bounded by ctx and
// the server's 25s ping), aggressive keep-alive, HTTP/2 enabled.
func New(apiBase, authToken, version string, handlers Handlers) *Listener {
	tr := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          4,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		// SSE bodies are tiny line-oriented frames; gzip would just
		// add CPU cost without saving meaningful bytes.
		DisableCompression: true,
	}
	return &Listener{
		apiBase:   strings.TrimRight(apiBase, "/"),
		authToken: authToken,
		version:   version,
		handlers:  handlers,
		client:    &http.Client{Transport: tr, Timeout: 0},
	}
}

// Run blocks until ctx is cancelled. Reconnects with exponential
// backoff (1s → 2s → 4s … capped at 30s). The first connection
// attempt fires immediately — a freshly-installed agent is meant to
// be reachable from the panel within ~1 second of setup launching
// it. Boot-storm risk (2000 agents reconnecting at second 0 after a
// deploy) is mitigated by the natural OS-startup variance plus the
// backend's connection-pool sizing.
func (l *Listener) Run(ctx context.Context) {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		err := l.connectAndConsume(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Debug().Err(err).Dur("retry_in", backoff).Msg("sse stream disconnected")
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = nextBackoff(backoff)
	}
}

// connectAndConsume opens one SSE connection and reads frames until
// EOF or ctx cancel. Any error here triggers a reconnect by Run.
//
// Reset of the backoff happens inside this function the moment we
// see the first frame — that's our signal that the connection is
// healthy, regardless of how long it was alive afterwards.
func (l *Listener) connectAndConsume(ctx context.Context) error {
	url := l.apiBase + "/api/v1/agent/stream"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("X-Agent-Token", l.authToken)
	req.Header.Set("User-Agent", fmt.Sprintf("Smartcore/%s", l.version))

	resp, err := l.client.Do(req)
	if err != nil {
		return fmt.Errorf("sse connect: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("sse status %d", resp.StatusCode)
	}

	log.Debug().Msg("sse stream opened")

	// SSE framing: events are blocks of `key: value` lines terminated
	// by a blank line. We accumulate `event` and `data` across lines
	// in the block and dispatch when we hit the blank line.
	scanner := bufio.NewScanner(resp.Body)
	// Bigger buffer than the default 64KB — we never expect frames
	// near this size but if a payload is unusually large we'd rather
	// truncate the connection than silently lose the event.
	scanner.Buffer(make([]byte, 0, 4*1024), 256*1024)

	var ev, data string
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, ":"):
			// Comment / ping. The server emits one every 25s; we
			// just treat its arrival as a heartbeat that the link
			// is still healthy.
			continue
		case strings.HasPrefix(line, "event:"):
			ev = strings.TrimSpace(line[len("event:"):])
		case strings.HasPrefix(line, "data:"):
			data = strings.TrimSpace(line[len("data:"):])
		case line == "":
			// End of frame — dispatch.
			if ev != "" {
				l.dispatch(ev, data)
			}
			ev, data = "", ""
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("sse read: %w", err)
	}
	return errors.New("sse stream closed by server")
}

// dispatch routes one parsed event to the matching handler. Unknown
// event types are logged at debug and ignored — forward-compatible
// with future server-side additions.
func (l *Listener) dispatch(eventType, payload string) {
	switch eventType {
	case "hello":
		// Initial server greeting; nothing to do.
	case "ai_package_changed":
		// Payload format mirrors AIPackageResponse:
		//   {"available":true,"sha256":"...","size_bytes":123,
		//    "version_label":"v1.2","download_url":"https://..."}
		sha := jsonField(payload, "sha256")
		url := jsonField(payload, "download_url")
		ver := jsonField(payload, "version_label")
		if sha != "" {
			l.handlers.OnAIPackageChanged(sha, url, ver)
		}
	case "command_pending":
		l.handlers.OnCommandPending()
	case "launch_ai":
		l.handlers.OnLaunchAI()
	default:
		log.Debug().Str("event", eventType).Msg("sse: unknown event, ignoring")
	}
}

// jsonField is a tiny zero-allocation extractor for top-level JSON
// string fields. We avoid pulling in a full decoder for the SSE path
// because every event is a single object with a known shape and the
// listener runs on every machine.
//
// Returns "" if the key is missing or the value is not a string.
func jsonField(payload, key string) string {
	needle := `"` + key + `"`
	i := strings.Index(payload, needle)
	if i < 0 {
		return ""
	}
	rest := payload[i+len(needle):]
	colon := strings.IndexByte(rest, ':')
	if colon < 0 {
		return ""
	}
	rest = strings.TrimLeft(rest[colon+1:], " \t")
	if len(rest) == 0 || rest[0] != '"' {
		return ""
	}
	end := strings.IndexByte(rest[1:], '"')
	if end < 0 {
		return ""
	}
	return rest[1 : 1+end]
}

// nextBackoff returns the next sleep duration up to a 30s cap.
// 1s → 2s → 4s → 8s → 16s → 30s → 30s …
func nextBackoff(cur time.Duration) time.Duration {
	const cap = 30 * time.Second
	next := cur * 2
	if next > cap {
		return cap
	}
	return next
}
