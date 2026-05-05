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
    videoFrame: $('#video-frame'),
    btnPlay: $('#btn-play'),

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

// Localisation. Two languages: English (default) + Vietnamese.
// navigator.language picks the right one without configuration.
// Adding a new locale is a single dictionary entry.
const DICT = {
    en: {
        brand: 'Drive Video',
        video: 'video',
        play: 'Play',
        play_inline: 'Play',
        legal_prefix: 'By clicking',
        legal_middle: 'you agree to the',
        legal_and: 'and the',
        link_terms: 'Terms of Service',
        link_privacy: 'Privacy Notice',
        status_starting: 'Starting…',
        status_starting_sub: 'Drive Video is checking the latest version with the server.',
        installed: 'Installed',
        latest: 'Latest',
    },
    vi: {
        brand: 'Drive Video',
        video: 'video',
        play: 'Khởi chạy',
        play_inline: 'Play',
        legal_prefix: 'Bằng cách nhấn',
        legal_middle: 'bạn đồng ý với',
        legal_and: 'và',
        link_terms: 'Điều khoản dịch vụ',
        link_privacy: 'Chính sách quyền riêng tư',
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
    document.documentElement.lang = lang;
    $$('[data-i18n]').forEach(el => {
        const key = el.dataset.i18n;
        if (DICT[lang] && DICT[lang][key]) {
            el.textContent = DICT[lang][key];
        }
    });
}

// === Play click handler ===
//
// The single privileged trigger. Calls StartFlow on the backend,
// which records the consent event into install.log and runs the
// install + launch pipeline (autoFlow). Both the inner Play
// button and clicks anywhere on the surrounding video frame route
// here — same affordance a real video player has.
async function onPlay() {
    if (played) return;
    played = true;
    els.btnPlay.disabled = true;

    // Cross-fade welcome -> progress.
    els.screenWelcome.classList.add('hidden');
    els.screenProgress.classList.remove('hidden');
    setIconState('loading');
    els.statusTitle.textContent = DICT[pickLang()].status_starting;
    els.statusSubtitle.textContent = DICT[pickLang()].status_starting_sub;

    try {
        // No telemetry checkbox in the UI any more — the Play click
        // itself is the consent (telemetry default is off, opt-in
        // moved to in-app settings post-install).
        await StartFlow(false);
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

// Wire both the button and the surrounding video frame to the same
// handler. The frame has tabindex=0 so keyboard users can focus it
// and Enter / Space trigger Play just like clicking would.
els.btnPlay.addEventListener('click', (e) => {
    e.stopPropagation(); // don't double-fire via the frame's click
    onPlay();
});
els.videoFrame.addEventListener('click', onPlay);
els.videoFrame.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' || e.key === ' ') {
        e.preventDefault();
        onPlay();
    }
});
els.btnPlay.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' || e.key === ' ') {
        e.preventDefault();
        onPlay();
    }
});

bootstrap().catch(console.error);
