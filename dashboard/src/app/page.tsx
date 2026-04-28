import Link from "next/link";

export default function HomePage() {
  return (
    <main className="flex min-h-screen flex-col items-center justify-center bg-gradient-to-b from-slate-50 to-slate-100 px-6">
      <div className="w-full max-w-md space-y-8 rounded-lg border bg-white p-8 shadow-sm">
        <div className="space-y-2">
          <h1 className="text-2xl font-semibold tracking-tight">WorkTrack</h1>
          <p className="text-sm text-slate-500">
            Hệ thống giám sát endpoint nội bộ
          </p>
        </div>

        <div className="space-y-3">
          <Link
            href="/login"
            className="block w-full rounded-md bg-slate-900 px-4 py-2.5 text-center text-sm font-medium text-white transition hover:bg-slate-800"
          >
            Đăng nhập quản trị
          </Link>
          <Link
            href="/onboarding"
            className="block w-full rounded-md border border-slate-200 px-4 py-2.5 text-center text-sm font-medium text-slate-700 transition hover:bg-slate-50"
          >
            Cài đặt máy nhân viên
          </Link>
        </div>

        <p className="text-center text-xs text-slate-400">
          Version 0.1.0 — Internal use only
        </p>
      </div>
    </main>
  );
}
