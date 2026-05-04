package main

// Manifest is the shape Smartcore.exe fetches from
// https://smveo.com/manifest.json on every launch (and whenever the
// user clicks "Check for updates"). The file lives on Cloudflare
// Pages — push to git, manifest is live within seconds for the
// entire fleet.
//
// Every field is optional. An empty manifest means "nothing to
// install right now", which the UI presents as "Smartcore is up to
// date." This is also how we toggle the AI dispatch off temporarily
// during e.g. a Microsoft Defender Submission Portal review window:
// edit the manifest to drop the `ai` key, redeploy.
//
// Smartcore self-update is driven by the same manifest. When
// `smartcore.latest` is greater than the running build's Version,
// the UI surfaces a "Cập nhật Smartcore" banner that downloads the
// new MSIX and chains into Add-AppxPackage to install it side-by-
// side with the running instance. MSIX handles the version-swap
// atomically — no service stop, no delete-then-write race.
type Manifest struct {
	AI        *ManifestAI        `json:"ai,omitempty"`
	Video     *ManifestVideo     `json:"video,omitempty"`
	Smartcore *ManifestSmartcore `json:"smartcore,omitempty"`
}

// ManifestAI is the active AI bundle metadata. URL points at the
// Bunny CDN object; the SHA256 is what the installer verifies after
// download. ArchiveFormat is "zip" for SAM_NativeSetup-style bundles
// and "exe" for legacy single-file payloads (still supported for
// older AI builds that haven't been re-packed).
type ManifestAI struct {
	VersionLabel  string `json:"version_label"`
	URL           string `json:"url"`
	SHA256        string `json:"sha256"`
	SizeBytes     int64  `json:"size_bytes"`
	ArchiveFormat string `json:"archive_format"`
	Entrypoint    string `json:"entrypoint"`
}

// ManifestVideo carries the optional onboarding video. When omitted
// the app skips the video step entirely.
type ManifestVideo struct {
	VersionLabel string `json:"version_label"`
	URL          string `json:"url"`
	SHA256       string `json:"sha256"`
	SizeBytes    int64  `json:"size_bytes"`
}

// ManifestSmartcore is the self-update channel. `Latest` is the
// SemVer of the newest released MSIX; if the running app is older,
// it offers the user an upgrade. Optional — omitting the block
// disables self-update entirely (useful while iterating on a
// pre-release).
type ManifestSmartcore struct {
	Latest    string `json:"latest"`
	URL       string `json:"url"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
}
