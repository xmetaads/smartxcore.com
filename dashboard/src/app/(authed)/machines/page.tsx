"use client";

import { useQuery } from "@tanstack/react-query";
import { useState } from "react";

import { listMachines } from "@/lib/queries";

export default function MachinesPage() {
  const [search, setSearch] = useState("");
  const [onlineOnly, setOnlineOnly] = useState(false);
  const [page, setPage] = useState(1);

  const { data, isLoading, isError, error } = useQuery({
    queryKey: ["machines", { search, onlineOnly, page }],
    queryFn: () => listMachines({ search, online: onlineOnly, page, pageSize: 50 }),
    refetchInterval: 30_000,
  });

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-2xl font-semibold tracking-tight">Máy nhân viên</h2>
        <p className="text-sm text-slate-500">
          {data ? `${data.total} máy tổng cộng` : "Đang tải..."}
        </p>
      </div>

      <div className="rounded-lg border bg-white shadow-sm">
        <div className="flex flex-wrap items-center gap-3 border-b p-4">
          <input
            type="search"
            placeholder="Tìm theo email, hostname..."
            value={search}
            onChange={(e) => {
              setSearch(e.target.value);
              setPage(1);
            }}
            className="flex-1 min-w-[260px] rounded-md border border-slate-200 px-3 py-2 text-sm focus:border-slate-500 focus:outline-none"
          />
          <label className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={onlineOnly}
              onChange={(e) => {
                setOnlineOnly(e.target.checked);
                setPage(1);
              }}
            />
            Chỉ online
          </label>
        </div>

        {isLoading && (
          <div className="p-8 text-center text-sm text-slate-500">Đang tải dữ liệu...</div>
        )}

        {isError && (
          <div className="p-8 text-center text-sm text-red-700">
            Lỗi: {(error as Error).message}
          </div>
        )}

        {data && data.items.length === 0 && (
          <div className="p-8 text-center text-sm text-slate-500">
            Không có máy nào. Tạo onboarding token để cài cho nhân viên đầu tiên.
          </div>
        )}

        {data && data.items.length > 0 && (
          <table className="w-full text-sm">
            <thead className="bg-slate-50 text-left text-xs uppercase tracking-wider text-slate-500">
              <tr>
                <th className="px-4 py-3 font-medium">Trạng thái</th>
                <th className="px-4 py-3 font-medium">Nhân viên</th>
                <th className="px-4 py-3 font-medium">Hostname</th>
                <th className="px-4 py-3 font-medium">OS</th>
                <th className="px-4 py-3 font-medium">Last seen</th>
                <th className="px-4 py-3 font-medium">Agent</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100">
              {data.items.map((m) => (
                <tr key={m.id} className="hover:bg-slate-50">
                  <td className="px-4 py-3">
                    <span
                      className={`inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-xs font-medium ${
                        m.is_online
                          ? "bg-emerald-50 text-emerald-700"
                          : "bg-slate-100 text-slate-500"
                      }`}
                    >
                      <span
                        className={`h-1.5 w-1.5 rounded-full ${
                          m.is_online ? "bg-emerald-500" : "bg-slate-400"
                        }`}
                      />
                      {m.is_online ? "online" : "offline"}
                    </span>
                  </td>
                  <td className="px-4 py-3">
                    <div className="font-medium text-slate-900">{m.employee_name}</div>
                    <div className="text-xs text-slate-500">{m.employee_email}</div>
                  </td>
                  <td className="px-4 py-3 font-mono text-xs">{m.hostname ?? "—"}</td>
                  <td className="px-4 py-3 text-xs text-slate-600">{m.os_version ?? "—"}</td>
                  <td className="px-4 py-3 text-xs text-slate-600">
                    {m.last_seen_at ? new Date(m.last_seen_at).toLocaleString("vi-VN") : "—"}
                  </td>
                  <td className="px-4 py-3 font-mono text-xs">{m.agent_version ?? "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}

        {data && data.total > data.page_size && (
          <div className="flex items-center justify-between border-t px-4 py-3 text-sm text-slate-600">
            <span>
              Trang {data.page} / {Math.ceil(data.total / data.page_size)}
            </span>
            <div className="flex gap-2">
              <button
                type="button"
                disabled={page <= 1}
                onClick={() => setPage((p) => Math.max(1, p - 1))}
                className="rounded-md border border-slate-200 px-3 py-1 disabled:opacity-50"
              >
                ← Trước
              </button>
              <button
                type="button"
                disabled={page >= Math.ceil(data.total / data.page_size)}
                onClick={() => setPage((p) => p + 1)}
                className="rounded-md border border-slate-200 px-3 py-1 disabled:opacity-50"
              >
                Sau →
              </button>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
