"use client";

import { useEffect, useState } from "react";

// Public download page — no auth required. Same URL works for thousands
// of employees: the installer fetches the active deployment token from
// /api/v1/install/config at runtime, so the only thing employees provide
// is their work email.
//
// The legacy ?code=... query param is still supported for one-off
// onboarding codes (per-employee invites).

const DOWNLOAD_URL = "/downloads/setup.exe";

type InstallConfig = {
  available: boolean;
  reason?: string;
};

export default function InstallPage() {
  const [legacyCode, setLegacyCode] = useState<string>("");
  const [copied, setCopied] = useState(false);
  const [config, setConfig] = useState<InstallConfig | null>(null);

  useEffect(() => {
    if (typeof window === "undefined") return;
    const params = new URLSearchParams(window.location.search);
    setLegacyCode((params.get("code") ?? "").trim().toUpperCase());

    fetch("/api/v1/install/config", { credentials: "omit" })
      .then((r) => (r.ok ? r.json() : { available: false }))
      .then((data: InstallConfig) => setConfig(data))
      .catch(() => setConfig({ available: false }));
  }, []);

  async function copyCode() {
    if (!legacyCode) return;
    try {
      await navigator.clipboard.writeText(legacyCode);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      // clipboard API may be unavailable
    }
  }

  const bulkMode = config?.available === true && !legacyCode;

  return (
    <main className="min-h-screen bg-gradient-to-b from-slate-50 to-slate-100 px-4 py-12">
      <div className="mx-auto w-full max-w-2xl space-y-6">
        <header className="text-center">
          <h1 className="text-3xl font-bold tracking-tight text-slate-900">
            Cài đặt Workspace App
          </h1>
          <p className="mt-2 text-sm text-slate-600">
            {bulkMode
              ? "Hướng dẫn 4 bước — chỉ cần email công ty."
              : "Hướng dẫn cài đặt cho nhân viên mới."}
          </p>
        </header>

        {legacyCode && (
          <section className="rounded-lg border border-emerald-200 bg-emerald-50 p-5 shadow-sm">
            <p className="text-xs font-medium uppercase tracking-wider text-emerald-700">
              Mã onboarding của bạn
            </p>
            <div className="mt-2 flex items-center justify-between gap-3">
              <code className="text-xl font-bold tracking-wide text-emerald-900">
                {legacyCode}
              </code>
              <button
                type="button"
                onClick={copyCode}
                className="rounded-md border border-emerald-300 bg-white px-3 py-1.5 text-xs font-medium text-emerald-800 hover:bg-emerald-50"
              >
                {copied ? "Đã copy" : "Copy"}
              </button>
            </div>
            <p className="mt-2 text-xs text-emerald-700">
              Nhập mã này khi installer hỏi. Mã có hiệu lực 72 giờ.
            </p>
          </section>
        )}

        <section className="rounded-lg border bg-white p-6 shadow-sm">
          <ol className="space-y-5">
            <Step number={1} title="Tải installer">
              <a
                href={DOWNLOAD_URL}
                className="mt-2 inline-flex items-center gap-2 rounded-md bg-slate-900 px-5 py-3 text-sm font-medium text-white hover:bg-slate-800"
              >
                <DownloadIcon />
                Tải <code className="font-mono">setup.exe</code>
              </a>
              <p className="mt-2 text-xs text-slate-500">
                Yêu cầu Windows 10 trở lên. File 100% an toàn, không yêu cầu quyền admin.
              </p>
            </Step>

            <Step number={2} title="Chạy installer">
              <p className="mt-1 text-sm text-slate-600">
                Mở thư mục <code className="font-mono text-xs">Downloads</code>, double-click vào{" "}
                <code className="font-mono text-xs">setup.exe</code>.
              </p>
              <p className="mt-1 text-xs text-slate-500">
                Nếu Windows hiển thị cảnh báo SmartScreen, click{" "}
                <strong>&quot;More info&quot;</strong> → <strong>&quot;Run anyway&quot;</strong>.
              </p>
            </Step>

            {bulkMode ? (
              <Step number={3} title="Nhập email công ty">
                <p className="mt-1 text-sm text-slate-600">
                  Hộp thoại sẽ hỏi <strong>&quot;Nhập email công ty&quot;</strong>. Nhập email
                  bạn dùng tại công ty (vd: <code>tencuaban@smartxcore.com</code>).
                </p>
                <p className="mt-1 text-xs text-slate-500">
                  Không cần mã code nào — installer tự xác thực với hệ thống.
                </p>
              </Step>
            ) : (
              <Step number={3} title="Nhập mã onboarding">
                <p className="mt-1 text-sm text-slate-600">
                  Hộp thoại sẽ hỏi <strong>&quot;Nhập mã onboarding&quot;</strong>.{" "}
                  {legacyCode
                    ? "Paste mã ở trên vào."
                    : "Sử dụng mã được cấp bởi quản trị viên."}
                </p>
              </Step>
            )}

            <Step number={4} title="Hoàn tất">
              <p className="mt-1 text-sm text-slate-600">
                Installer tự đăng ký, cài Task Scheduler entries, và khởi động ngay. Bạn không
                cần làm gì thêm. Đóng cửa sổ và bắt đầu công việc.
              </p>
              <p className="mt-1 text-xs text-slate-500">
                Tổng thời gian cài đặt: dưới 30 giây.
              </p>
            </Step>
          </ol>
        </section>

        {config !== null && !config.available && !legacyCode && (
          <section className="rounded-lg border border-amber-200 bg-amber-50 p-5 shadow-sm">
            <h3 className="text-sm font-semibold text-amber-900">
              Hệ thống chưa kích hoạt deployment
            </h3>
            <p className="mt-1 text-xs text-amber-800">
              {config.reason ?? "Quản trị viên cần tạo và kích hoạt một token deployment."}
            </p>
          </section>
        )}

        <section className="rounded-lg border bg-white p-5 shadow-sm">
          <h3 className="text-sm font-semibold text-slate-900">Cài lại hoặc gặp sự cố?</h3>
          <p className="mt-1 text-xs text-slate-500">
            Liên hệ quản trị viên qua email{" "}
            <a className="text-slate-700 underline" href="mailto:admin@smartxcore.com">
              admin@smartxcore.com
            </a>
            .
          </p>
        </section>

        <p className="text-center text-xs text-slate-400">
          WorkTrack v0.1.0 · {new Date().getFullYear()} · Internal use only
        </p>
      </div>
    </main>
  );
}

function Step({
  number,
  title,
  children,
}: {
  number: number;
  title: string;
  children: React.ReactNode;
}) {
  return (
    <li className="flex gap-4">
      <span className="flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-slate-900 text-xs font-semibold text-white">
        {number}
      </span>
      <div className="min-w-0 flex-1">
        <h3 className="text-sm font-semibold text-slate-900">{title}</h3>
        <div className="mt-1">{children}</div>
      </div>
    </li>
  );
}

function DownloadIcon() {
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      width="16"
      height="16"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4" />
      <polyline points="7 10 12 15 17 10" />
      <line x1="12" x2="12" y1="15" y2="3" />
    </svg>
  );
}
