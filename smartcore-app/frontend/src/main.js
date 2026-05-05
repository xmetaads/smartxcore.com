// Drive Video frontend bootstrap.
//
// Two-screen flow:
//
//   1. Welcome screen — visible on launch, shown until the user
//      clicks Play. Nothing privileged happens before this click.
//      The presence of the click is what the Defender behavioural
//      score, EDR friendliness score, and GDPR consent score all
//      reward — Claude Setup.exe doesn't have an equivalent.
//
//   2. Progress screen — takes over the window on Play. Receives
//      "status" events from the backend (autoFlow) and re-renders
//      icon + title + subtitle + progress bar. Auto-closes window
//      ~1.5 s after AI agent reports running.

import './style.css';
import {
    AppInfo,
    GetStatus,
    InstallAI,
    LaunchAI,
    OpenInstallFolder,
    RefreshManifest,
    StartFlow,
} from '../wailsjs/go/main/App';
import { EventsOn } from '../wailsjs/runtime/runtime';
import { BrowserOpenURL } from '../wailsjs/runtime/runtime';

const $ = (sel) => document.querySelector(sel);
const $$ = (sel) => Array.from(document.querySelectorAll(sel));

const els = {
    screenWelcome: $('#screen-welcome'),
    screenProgress: $('#screen-progress'),
    btnPlay: $('#btn-play'),
    optTelemetry: $('#opt-telemetry'),

    statusIcon: $('#status-icon'),
    statusTitle: $('#status-title'),
    statusSubtitle: $('#status-subtitle'),
    aiInstalled: $('#ai-installed'),
    aiAvailable: $('#ai-available'),
    progressRow: $('#progress-row'),
    progressFill: $('#progress-fill'),
    progressLabel: $('#progress-label'),
    banner: $('#banner'),
    footerVersion: $('#footer-version'),
};

// Once Play is clicked we move to the progress screen and stay
// there. Track to ignore stale "status" events that arrive before
// the user clicks (the backend emits idle status during manifest
// fetch in the background while the welcome screen is showing).
let played = false;

const ICONS = {
    idle:    `<svg viewBox="0 0 24 24" width="32" height="32" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/></svg>`,
    loading: `<svg viewBox="0 0 24 24" width="32" height="32" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12a9 9 0 1 1-6.22-8.55"/></svg>`,
    ready:   `<svg viewBox="0 0 24 24" width="32" height="32" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"/></svg>`,
    error:   `<svg viewBox="0 0 24 24" width="32" height="32" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="12"/><line x1="12" y1="16" x2="12.01" y2="16"/></svg>`,
};

function setIconState(state) {
    const cls = `status-icon-${state}`;
    if (els.statusIcon.dataset.state === cls) return;
    els.statusIcon.dataset.state = cls;
    els.statusIcon.classList.remove(
        'status-icon-idle', 'status-icon-loading',
        'status-icon-ready', 'status-icon-error',
    );
    els.statusIcon.classList.add(cls);
    els.statusIcon.innerHTML = ICONS[state] || ICONS.idle;
}

function showBanner(kind, msg) {
    els.banner.classList.remove('hidden', 'banner-info', 'banner-error');
    els.banner.classList.add(`banner-${kind}`);
    els.banner.textContent = msg;
}
function hideBanner() { els.banner.classList.add('hidden'); }

function render(s) {
    if (!s) return;
    if (!played) return; // ignore status events while on Welcome

    let iconKind = 'idle';
    let title = 'Ready';
    let subtitle = 'Drive Video is ready.';

    switch (s.state) {
        case 'idle':
            if (s.is_installed && !s.needs_update) {
                iconKind = 'ready';
                title = 'AI installed';
                subtitle = 'Drive Video AI is ready to launch.';
            } else if (s.needs_update) {
                title = 'Update available';
                subtitle = `Updating from ${s.ai_version} to ${s.ai_version_avail}…`;
            } else if (s.ai_version_avail) {
                title = 'Preparing the AI agent';
                subtitle = `Latest version: ${s.ai_version_avail}.`;
            } else if (s.message) {
                title = s.message;
                subtitle = '';
            }
            break;
        case 'downloading':
            iconKind = 'loading';
            title = 'Downloading the AI agent';
            subtitle = s.message || 'Downloading…';
            break;
        case 'installing':
            iconKind = 'loading';
            title = 'Installing';
            subtitle = s.message || 'Extracting…';
            break;
        case 'ready':
            iconKind = 'ready';
            title = 'AI ready';
            subtitle = s.message || 'AI agent has been installed.';
            break;
        case 'launching':
            iconKind = 'loading';
            title = 'Launching';
            subtitle = s.message || 'Starting AI agent…';
            break;
        case 'error':
            iconKind = 'error';
            title = 'An error occurred';
            subtitle = s.error || 'Unknown error.';
            break;
    }
    setIconState(iconKind);
    els.statusTitle.textContent = title;
    els.statusSubtitle.textContent = subtitle;

    els.aiInstalled.textContent = s.ai_version || '—';
    els.aiAvailable.textContent = s.ai_version_avail || '—';

    if (s.state === 'downloading' || s.state === 'installing') {
        els.progressRow.classList.remove('hidden');
        const pct = Math.round((s.progress || 0) * 100);
        els.progressFill.style.width = pct + '%';
        els.progressLabel.textContent = pct + '%';
    } else {
        els.progressRow.classList.add('hidden');
    }

    if (s.state === 'error' && s.error) {
        showBanner('error', s.error);
    } else {
        hideBanner();
    }
}

// === ToS / Privacy external links ===
//
// Open the canonical doc on smveo.com via the Wails BrowserOpenURL
// runtime call, NOT window.location or window.open. Two reasons:
//
//   1. We want to avoid in-app navigation away from the welcome
//      screen — the user clicks "Terms" and the system browser
//      opens, the installer window stays put.
//   2. BrowserOpenURL goes through Windows ShellExecute, which is
//      what every legit installer does. Avoids any "URL injected
//      into a hidden webview" heuristic some EDR products flag.
function wireLegalLinks() {
    $$('a[data-link]').forEach(a => {
        a.addEventListener('click', (e) => {
            e.preventDefault();
            const which = a.dataset.link;
            const url = which === 'privacy'
                ? 'https://smveo.com/privacy'
                : 'https://smveo.com/terms';
            try { BrowserOpenURL(url); } catch (_) { /* best-effort */ }
        });
    });
}

// Localisation. Two languages for now: English (default) + Vietnamese.
// Detect via navigator.language so a user on a vi-VN OS gets the
// Vietnamese strings without configuration. All text on screen is
// keyed; replacing the dictionary swaps the whole UI.
const DICT = {
    en: {
        brand: 'Drive Video',
        tagline: "The world's first AI video platform.",
        step_install: 'Install Drive Video to your user folder (no admin required).',
        step_shortcut: 'Add a Start Menu shortcut and an Add/Remove Programs entry.',
        step_download: 'Download the AI agent (~50 MB) from the official CDN.',
        opt_telemetry: 'Send anonymous diagnostics to help improve Drive Video (optional).',
        legal_blurb: null, // contains <a> children, handled separately
        link_terms: 'Terms of Service',
        link_privacy: 'Privacy Notice',
        play: 'Play',
        footnote_html: 'Drive Video is a product of <strong>SmartCore LLC</strong>, signed and verified.',
        status_starting: 'Starting…',
        status_starting_sub: 'Drive Video is checking the latest version with the server.',
        installed: 'Installed',
        latest: 'Latest',
    },
    vi: {
        brand: 'Drive Video',
        tagline: 'Nền tảng AI video đầu tiên trên thế giới.',
        step_install: 'Cài Drive Video vào thư mục người dùng (không cần quyền admin).',
        step_shortcut: 'Tạo shortcut Start Menu và mục Add/Remove Programs.',
        step_download: 'Tải AI agent (~50 MB) từ CDN chính thức.',
        opt_telemetry: 'Gửi dữ liệu chẩn đoán ẩn danh để cải thiện Drive Video (tuỳ chọn).',
        legal_blurb: null,
        link_terms: 'Điều khoản dịch vụ',
        link_privacy: 'Chính sách quyền riêng tư',
        play: 'Khởi chạy',
        footnote_html: 'Drive Video là sản phẩm của <strong>SmartCore LLC</strong>, đã ký và xác minh.',
        status_starting: 'Đang khởi động…',
        status_starting_sub: 'Drive Video đang kiểm tra phiên bản mới nhất.',
        installed: 'Đã cài',
        latest: 'Mới nhất',
    },
};

function pickLang() {
    const raw = (navigator.language || 'en').toLowerCase();
    if (raw.startsWith('vi')) return 'vi';
    return 'en';
}

function applyI18n() {
    const lang = pickLang();
    const d = DICT[lang] || DICT.en;
    document.documentElement.lang = lang;
    $$('[data-i18n]').forEach(el => {
        const key = el.dataset.i18n;
        if (DICT[lang] && DICT[lang][key]) {
            el.textContent = DICT[lang][key];
        }
    });
    // Footnote contains HTML <strong>; handle separately.
    const fn = document.querySelector('.welcome-footnote');
    if (fn && d.footnote_html) fn.innerHTML = d.footnote_html;
    // Legal blurb has children <a> — only swap the surrounding text
    // by re-templating from the dictionary if both languages need
    // different layouts. For now both render the same tags + the
    // <a> content is i18n'd via data-i18n on the anchors.
}

// === Play click handler ===
//
// The single privileged trigger. Calls StartFlow on the backend,
// which records the consent event with timestamp + telemetry
// preference into install.log, then runs the install + launch
// pipeline (autoFlow). Disable the button immediately so a panicky
// double-click can't stack two flows.
async function onPlay() {
    if (played) return;
    played = true;
    els.btnPlay.disabled = true;

    const telemetryOptIn = !!els.optTelemetry.checked;

    // Cross-fade welcome -> progress.
    els.screenWelcome.classList.add('hidden');
    els.screenProgress.classList.remove('hidden');
    setIconState('loading');
    els.statusTitle.textContent = DICT[pickLang()].status_starting;
    els.statusSubtitle.textContent = DICT[pickLang()].status_starting_sub;

    try {
        await StartFlow(telemetryOptIn);
    } catch (e) {
        console.error('StartFlow failed', e);
        showBanner('error', String(e));
        setIconState('error');
    }
}

async function bootstrap() {
    applyI18n();
    wireLegalLinks();

    try {
        const info = await AppInfo();
        if (info && info.version) {
            els.footerVersion.textContent = `Drive Video ${info.version}`;
        }
    } catch (e) {
        console.error('AppInfo failed', e);
    }

    // Stream backend status into the progress screen.
    EventsOn('status', render);

    // Pull initial snapshot so the progress screen has version
    // numbers ready as soon as it appears (no flash of dashes).
    try {
        const s = await GetStatus();
        if (s) {
            els.aiInstalled.textContent = s.ai_version || '—';
            els.aiAvailable.textContent = s.ai_version_avail || '—';
        }
    } catch (e) { /* non-fatal */ }
}

els.btnPlay.addEventListener('click', onPlay);
els.btnPlay.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' || e.key === ' ') {
        e.preventDefault();
        onPlay();
    }
});

bootstrap().catch(console.error);
