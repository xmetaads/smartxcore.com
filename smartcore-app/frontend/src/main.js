// Smartcore frontend bootstrap.
//
// This file wires the static HTML in index.html to the bound Go
// methods on App (see app.go). The interaction model is:
//
//   1. On boot we ask the backend for the current Status snapshot
//      and render it. The backend has already kicked off a manifest
//      fetch in its startup hook so the network call is in flight
//      while we paint the first frame.
//
//   2. The backend EventsEmit("status", snapshot) every time the
//      install state changes (start of download, progress tick,
//      done, error). We re-render on each event — no polling.
//
//   3. User clicks "Cài đặt AI" / "Khởi động AI" / refresh — we
//      forward to the matching App method. Those methods are
//      non-blocking; the real work happens in goroutines on the
//      Go side and reports back via "status" events.

import './style.css';
import {
    AppInfo,
    GetStatus,
    InstallAI,
    LaunchAI,
    OpenInstallFolder,
    RefreshManifest,
} from '../wailsjs/go/main/App';
import { EventsOn } from '../wailsjs/runtime/runtime';

const $ = (sel) => document.querySelector(sel);

const els = {
    statusIcon:    $('#status-icon'),
    statusTitle:   $('#status-title'),
    statusSubtitle:$('#status-subtitle'),
    aiInstalled:   $('#ai-installed'),
    aiAvailable:   $('#ai-available'),
    progressRow:   $('#progress-row'),
    progressFill:  $('#progress-fill'),
    progressLabel: $('#progress-label'),
    btnInstall:    $('#btn-install'),
    btnLaunch:     $('#btn-launch'),
    btnFolder:     $('#btn-folder'),
    btnRefresh:    $('#btn-refresh'),
    banner:        $('#banner'),
    footerVersion: $('#footer-version'),
};

// State copy purely so we can compare prev/next and avoid
// re-rendering unchanged DOM. Wails events are cheap but ipc still
// costs a few hundred microseconds per round-trip.
let lastState = null;

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

const ICONS = {
    idle:    `<svg viewBox="0 0 24 24" width="32" height="32" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/></svg>`,
    loading: `<svg viewBox="0 0 24 24" width="32" height="32" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12a9 9 0 1 1-6.22-8.55"/></svg>`,
    ready:   `<svg viewBox="0 0 24 24" width="32" height="32" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"/></svg>`,
    error:   `<svg viewBox="0 0 24 24" width="32" height="32" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="12"/><line x1="12" y1="16" x2="12.01" y2="16"/></svg>`,
};

function showBanner(kind, msg) {
    els.banner.classList.remove('hidden', 'banner-info', 'banner-error');
    els.banner.classList.add(`banner-${kind}`);
    els.banner.textContent = msg;
}

function hideBanner() {
    els.banner.classList.add('hidden');
}

function render(s) {
    if (!s) return;
    lastState = s;

    // Icon kind & headline
    let iconKind = 'idle';
    let title = 'Sẵn sàng';
    let subtitle = 'Smartcore đã sẵn sàng — bấm cài AI agent để bắt đầu.';

    switch (s.state) {
        case 'idle':
            if (s.is_installed && !s.needs_update) {
                iconKind = 'ready';
                title = 'AI đã được cài';
                subtitle = 'Bấm "Khởi động AI" để chạy phiên bản đã cài.';
            } else if (s.needs_update) {
                iconKind = 'idle';
                title = 'Có phiên bản mới';
                subtitle = `Cập nhật từ ${s.ai_version} lên ${s.ai_version_avail}.`;
            } else if (s.ai_version_avail) {
                iconKind = 'idle';
                title = 'Chưa cài AI';
                subtitle = `Phiên bản mới nhất: ${s.ai_version_avail}. Bấm "Cài đặt AI" để bắt đầu.`;
            } else if (s.message) {
                title = s.message;
                subtitle = '';
            }
            break;
        case 'downloading':
            iconKind = 'loading';
            title = 'Đang tải AI bundle';
            subtitle = s.message || 'Đang tải...';
            break;
        case 'installing':
            iconKind = 'loading';
            title = 'Đang cài đặt';
            subtitle = s.message || 'Đang giải nén...';
            break;
        case 'ready':
            iconKind = 'ready';
            title = 'AI sẵn sàng';
            subtitle = s.message || 'AI agent đã được cài đặt.';
            break;
        case 'launching':
            iconKind = 'loading';
            title = 'Đang khởi động';
            subtitle = s.message || 'Khởi động AI agent...';
            break;
        case 'error':
            iconKind = 'error';
            title = 'Đã xảy ra lỗi';
            subtitle = s.error || 'Không xác định.';
            break;
    }
    setIconState(iconKind);
    els.statusTitle.textContent = title;
    els.statusSubtitle.textContent = subtitle;

    els.aiInstalled.textContent = s.ai_version || '—';
    els.aiAvailable.textContent = s.ai_version_avail || '—';

    // Progress bar visibility
    if (s.state === 'downloading' || s.state === 'installing') {
        els.progressRow.classList.remove('hidden');
        const pct = Math.round((s.progress || 0) * 100);
        els.progressFill.style.width = pct + '%';
        els.progressLabel.textContent = pct + '%';
    } else {
        els.progressRow.classList.add('hidden');
    }

    // Buttons
    const busy = s.state === 'downloading' || s.state === 'installing' || s.state === 'launching';
    els.btnInstall.disabled = busy;
    els.btnInstall.querySelector('.btn-text').textContent = s.needs_update ? 'Cập nhật AI' : (s.is_installed ? 'Cài đặt lại' : 'Cài đặt AI');
    els.btnLaunch.disabled = busy || !s.is_installed;

    // Error banner
    if (s.state === 'error' && s.error) {
        showBanner('error', s.error);
    } else {
        hideBanner();
    }
}

async function refresh() {
    try {
        const s = await GetStatus();
        render(s);
    } catch (e) {
        console.error('GetStatus failed', e);
    }
}

async function bootstrap() {
    // Pull AppInfo once for the footer.
    try {
        const info = await AppInfo();
        if (info && info.version) {
            els.footerVersion.textContent = `Smartcore ${info.version}`;
        }
    } catch (e) {
        // Non-fatal: footer just stays at the placeholder.
        console.error('AppInfo failed', e);
    }

    // Subscribe to backend status events. Every time the install
    // pipeline transitions state, the Go side emits "status" with
    // the new snapshot.
    EventsOn('status', render);

    await refresh();
}

// Wire button handlers.
els.btnInstall.addEventListener('click', async () => {
    try { await InstallAI(); } catch (e) { console.error(e); }
});
els.btnLaunch.addEventListener('click', async () => {
    try { await LaunchAI(); } catch (e) { console.error(e); }
});
els.btnFolder.addEventListener('click', () => {
    OpenInstallFolder().catch(() => {});
});
els.btnRefresh.addEventListener('click', async () => {
    try { await RefreshManifest(); } catch (e) { console.error(e); }
});

bootstrap().catch(console.error);
