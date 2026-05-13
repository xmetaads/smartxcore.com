//go:build windows

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Manifest is the response shape from api.smveo.com/desktop/win32.
// Drives the bootstrapper's download + install pipeline.
//
// The server returns a JSON document. Example:
//
//   {
//     "version":              "1.0.0",
//     "msix_url":             "https://downloads.smveo.com/drivevideo/1.0.0/Drive Video.msix",
//     "msix_sha256":          "abc123...",
//     "msix_size":            121562112,
//     "package_family_name":  "SmartCoreLLC.DriveVideo_pzs8sxrjxfjjc",
//     "appinstaller_url":     "https://smveo.com/drivevideo/DriveVideo.appinstaller",
//     "min_windows_build":    17134
//   }
//
// All fields are required except min_windows_build (defaults to
// 17134 = Windows 10 1803, the earliest version that supports the
// AppInstaller XML auto-update flow).
type Manifest struct {
	Version           string `json:"version"`
	MsixURL           string `json:"msix_url"`
	MsixSHA256        string `json:"msix_sha256"`
	MsixSize          int64  `json:"msix_size"`
	PackageFamilyName string `json:"package_family_name"`
	AppInstallerURL   string `json:"appinstaller_url"`
	MinWindowsBuild   int    `json:"min_windows_build,omitempty"`
}

// fetchManifest pulls the release manifest from the configured
// API endpoint. Honours a proxy URL if one was detected from the
// system. 30-second hard timeout: the manifest is tiny JSON, if
// it doesn't return fast the user isn't reachable.
func fetchManifest(ctx context.Context, manifestURL, proxyURL string) (*Manifest, error) {
	tr := &http.Transport{}
	if proxyURL != "" {
		u, err := url.Parse(proxyURL)
		if err == nil {
			tr.Proxy = http.ProxyURL(u)
		}
	}
	cli := &http.Client{
		Transport: tr,
		Timeout:   30 * time.Second,
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("User-Agent", fmt.Sprintf("DriveVideoSetup/%s", Version))
	req.Header.Set("Accept", "application/json")

	resp, err := cli.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("manifest returned HTTP %d", resp.StatusCode)
	}

	// Cap the read at 64 KB — the manifest should be a few hundred
	// bytes; a much larger response means the server returned an
	// error page or has been compromised.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 65536))
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}

	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}

	if err := validateManifest(&m); err != nil {
		return nil, fmt.Errorf("invalid manifest: %w", err)
	}
	return &m, nil
}

// validateManifest sanity-checks the fields the bootstrapper
// actually uses. Fail-fast on garbage so we don't spend 5 minutes
// downloading and then realise the SHA is empty.
func validateManifest(m *Manifest) error {
	if m.Version == "" {
		return fmt.Errorf("missing version")
	}
	if m.MsixURL == "" {
		return fmt.Errorf("missing msix_url")
	}
	if m.MsixSHA256 == "" || len(m.MsixSHA256) != 64 {
		return fmt.Errorf("missing or malformed msix_sha256")
	}
	if m.MsixSize <= 0 {
		return fmt.Errorf("invalid msix_size")
	}
	if m.PackageFamilyName == "" {
		return fmt.Errorf("missing package_family_name")
	}
	return nil
}
