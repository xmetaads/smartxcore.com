"use client";

import { useQuery } from "@tanstack/react-query";

import { listAIPackages, listMachines } from "@/lib/queries";

// Live overview: pulls /api/v1/admin/machines + /api/v1/admin/ai-packages
// every 5s and computes the stat cards client-side. Keeps the backend
// surface tiny (no dedicated /admin/stats endpoint) and the numbers
// stay consistent with the per-page lists.
export default function DashboardPage() {
  // We only need the totals, not full pages — but the listing endpoint
  // already returns `total` separately from `items` so a single call
  // gives us active count + first 50 rows we can derive recency from.
  const machinesQuery = useQuery({
    queryKey: ["dashboard-machines-all"],
    queryFn: () => listMachines({ pageSize: 200 }),
    refetchInterval: 5_000,
  });

  const onlineQuery = useQuery({
    queryKey: ["dashboard-machines-online"],
    queryFn: () => listMachines({ online: true, pageSize: 200 }),
    refetchInterval: 5_000,
  });

  const aiQuery = useQuery({
    queryKey: ["dashboard-ai"],
    queryFn: () => listAIPackages(),
    refetchInterval: 30_000,
  });

  const total = machinesQuery.data?.total ?? null;
  const onlineCount = onlineQuery.data?.total ?? null;
  const offlineCount =
    total !== null && onlineCount !== null ? Math.max(total - onlineCount, 0) : null;

  // "Offline > 24h" = machines whose last_seen_at is older than 24 hours
  // (or never reported). Computed from the machines page payload.
  const now = Date.now();
  const oneDayMs = 24 * 60 * 60 * 1000;
  const offlineDay = (machinesQuery.data?.items ?? []).filter((m) => {
    if (m.is_online) return false;
    if (!m.last_seen_at) return true;
    return now - new Date(m.last_seen_at).getTime() > oneDayMs;
  }).length;

  const activeAI = (aiQuery.data?.items ?? []).find((p) => p.is_active);

  const cards: { label: string; value: string; sub?: string }[] = [
    {
      label: "Tổng số máy",
      value: total === null ? "…" : String(total),
      sub: total === 0 ? "Chưa có nhân viên cài đặt" : undefined,
    },
    {
      label: "Đang online",
      value: onlineCount === null ? "…" : String(onlineCount),
      sub:
        total !== null && total > 0 && onlineCount !== null
          ? `${Math.round((onlineCount / total) * 100)}% fleet`
          : undefined,
    },
    {
      label: "Offline",
      value: offlineCount === null ? "…" : String(offlineCount),
      sub: offlineDay > 0 ? `${offlineDay} máy offline > 24h` : undefined,
    },
    {
      label: "AI client active",
      value: activeAI ? activeAI.version_label : "—",
      sub: activeAI ? `SHA ${activeAI.sha256.slice(0, 12)}…` : "Chưa có phiên bản nào",
    },
  ];

  // Pick the most recent five machines (by last_seen_at, newest first)
  // for a tiny activity feed under the stat grid. Doesn't need its own
  // endpoint — the machines list is already sorted by online + last
  // seen on the server.
  const recent = (machinesQuery.data?.items ?? []).slice(0, 5);

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-2xl font-semibold tracking-tight">Tổng quan</h2>
        <p className="text-sm text-slate-500">
          Tình trạng fleet endpoint cập nhật mỗi 5 giây.
        </p>
      </div>

      <div className="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-4">
        {cards.map((c) => (
          <div key={c.label} className="rounded-lg border bg-white p-5 shadow-sm">
            <p className="text-sm font-medium text-slate-500">{c.label}</p>
            <p className="mt-2 text-3xl font-semibold text-slate-900">{c.value}</p>
            {c.sub && <p className="mt-1 text-xs text-slate-400">{c.sub}</p>}
          </div>
        ))}
      </div>

      <div className="rounded-lg border bg-white shadow-sm">
        <div className="border-b p-4">
          <h3 className="text-base font-medium">Hoạt động gần đây</h3>
          <p className="text-xs text-slate-500">
            5 máy có heartbeat mới nhất.
          </p>
        </div>
        {recent.length === 0 && !machinesQuery.isLoading && (
          <div className="p-8 text-center text-sm text-slate-500">
            Chưa có máy nào. Phát hành setup.exe cho nhân viên đầu tiên.
          </div>
        )}
        {recent.length > 0 && (
          <ul className="divide-y">
            {recent.map((m) => (
              <li key={m.id} className="flex items-center gap-3 px-4 py-3">
                <span
                  className={`inline-flex h-2 w-2 rounded-full ${
                    m.is_online ? "bg-emerald-500" : "bg-slate-300"
                  }`}
                />
                <div className="flex-1 min-w-0">
                  <p className="truncate text-sm font-medium text-slate-900">
                    {m.employee_name || m.employee_email}
                  </p>
                  <p className="truncate text-xs text-slate-500">
                    {m.hostname || "—"} · {m.os_version || "windows"}
                  </p>
                </div>
                <p className="text-xs text-slate-400">
                  {m.last_seen_at
                    ? new Date(m.last_seen_at).toLocaleString("vi-VN")
                    : "Chưa heartbeat"}
                </p>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}
