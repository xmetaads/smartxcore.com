"use client";

import { useMutation, useQueries, useQuery } from "@tanstack/react-query";
import { useState } from "react";

import { APIError } from "@/lib/api-client";
import {
  type Command,
  createCommand,
  getCommand,
  listMachines,
  type MachineSummary,
} from "@/lib/queries";

const PRESETS: Array<{ label: string; script: string }> = [
  { label: "Tên máy + user", script: "Write-Output \"$env:COMPUTERNAME\\$env:USERNAME\"" },
  { label: "Phiên bản Windows", script: "(Get-CimInstance Win32_OperatingSystem).Caption + ' build ' + (Get-CimInstance Win32_OperatingSystem).BuildNumber" },
  { label: "Disk free (GB)", script: "Get-PSDrive C | Select-Object Used,Free" },
  { label: "Top 5 process CPU", script: "Get-Process | Sort-Object CPU -Descending | Select-Object -First 5 Name,CPU,WS" },
];

export default function CommandsPage() {
  const [script, setScript] = useState("");
  const [timeoutSeconds, setTimeoutSeconds] = useState(300);
  const [selectedMachineIds, setSelectedMachineIds] = useState<string[]>([]);
  const [createdIds, setCreatedIds] = useState<string[]>([]);
  const [error, setError] = useState<string | null>(null);

  const machinesQuery = useQuery({
    queryKey: ["machines-for-commands"],
    queryFn: () => listMachines({ pageSize: 200 }),
  });

  const submit = useMutation({
    mutationFn: createCommand,
    onSuccess: (data) => {
      setCreatedIds(data.command_ids);
      setError(null);
    },
    onError: (err) => {
      setError(err instanceof APIError ? err.message : "Submit failed");
    },
  });

  function toggleMachine(id: string) {
    setSelectedMachineIds((prev) =>
      prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id],
    );
  }

  function selectAllOnline() {
    if (!machinesQuery.data) return;
    setSelectedMachineIds(
      machinesQuery.data.items.filter((m) => m.is_online).map((m) => m.id),
    );
  }

  function clearSelection() {
    setSelectedMachineIds([]);
  }

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setCreatedIds([]);
    setError(null);
    if (selectedMachineIds.length === 0) {
      setError("Chọn ít nhất 1 máy");
      return;
    }
    if (script.trim().length === 0) {
      setError("Nhập script PowerShell");
      return;
    }
    submit.mutate({
      machine_ids: selectedMachineIds,
      script_content: script,
      timeout_seconds: timeoutSeconds,
    });
  }

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-2xl font-semibold tracking-tight">PowerShell từ xa</h2>
        <p className="text-sm text-slate-500">
          Chạy script trên 1 hoặc nhiều máy. Mọi command đều được audit log.
        </p>
      </div>

      <form onSubmit={handleSubmit} className="space-y-6">
        <div className="rounded-lg border bg-white p-6 shadow-sm">
          <h3 className="text-base font-medium">Script</h3>

          <div className="mt-3 flex flex-wrap gap-2">
            {PRESETS.map((p) => (
              <button
                type="button"
                key={p.label}
                onClick={() => setScript(p.script)}
                className="rounded-md border border-slate-200 px-3 py-1 text-xs hover:bg-slate-50"
              >
                {p.label}
              </button>
            ))}
          </div>

          <textarea
            value={script}
            onChange={(e) => setScript(e.target.value)}
            rows={10}
            placeholder="Get-Process | Where-Object { $_.CPU -gt 100 }"
            className="mt-3 block w-full rounded-md border border-slate-200 px-3 py-2 font-mono text-sm focus:border-slate-500 focus:outline-none"
          />

          <div className="mt-3 flex items-center gap-3">
            <label className="text-xs text-slate-600">Timeout (giây):</label>
            <input
              type="number"
              min={10}
              max={3600}
              value={timeoutSeconds}
              onChange={(e) => setTimeoutSeconds(Number(e.target.value) || 300)}
              className="w-24 rounded-md border border-slate-200 px-2 py-1 text-sm"
            />
          </div>
        </div>

        <MachinePicker
          machines={machinesQuery.data?.items ?? []}
          isLoading={machinesQuery.isLoading}
          selectedIds={selectedMachineIds}
          onToggle={toggleMachine}
          onSelectOnline={selectAllOnline}
          onClear={clearSelection}
        />

        {error && (
          <div className="rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700">
            {error}
          </div>
        )}

        <div className="flex items-center justify-between">
          <span className="text-sm text-slate-500">
            Đã chọn {selectedMachineIds.length} máy
          </span>
          <button
            type="submit"
            disabled={submit.isPending}
            className="rounded-md bg-slate-900 px-5 py-2 text-sm font-medium text-white hover:bg-slate-800 disabled:opacity-50"
          >
            {submit.isPending ? "Đang gửi..." : "Chạy lệnh"}
          </button>
        </div>
      </form>

      {createdIds.length > 0 && <ResultsPanel commandIds={createdIds} />}
    </div>
  );
}

function MachinePicker({
  machines,
  isLoading,
  selectedIds,
  onToggle,
  onSelectOnline,
  onClear,
}: {
  machines: MachineSummary[];
  isLoading: boolean;
  selectedIds: string[];
  onToggle: (id: string) => void;
  onSelectOnline: () => void;
  onClear: () => void;
}) {
  const [search, setSearch] = useState("");
  const filtered = machines.filter((m) => {
    if (!search) return true;
    const q = search.toLowerCase();
    return (
      m.employee_email.toLowerCase().includes(q) ||
      m.employee_name.toLowerCase().includes(q) ||
      (m.hostname?.toLowerCase().includes(q) ?? false)
    );
  });

  return (
    <div className="rounded-lg border bg-white shadow-sm">
      <div className="flex flex-wrap items-center gap-3 border-b p-4">
        <h3 className="text-base font-medium">Máy đích</h3>
        <input
          type="search"
          placeholder="Tìm..."
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          className="flex-1 min-w-[200px] rounded-md border border-slate-200 px-3 py-1.5 text-sm"
        />
        <button
          type="button"
          onClick={onSelectOnline}
          className="rounded-md border border-slate-200 px-3 py-1 text-xs hover:bg-slate-50"
        >
          Chọn tất cả online
        </button>
        <button
          type="button"
          onClick={onClear}
          className="rounded-md border border-slate-200 px-3 py-1 text-xs hover:bg-slate-50"
        >
          Bỏ chọn
        </button>
      </div>

      {isLoading && <div className="p-6 text-center text-sm text-slate-500">Đang tải...</div>}

      {!isLoading && filtered.length === 0 && (
        <div className="p-6 text-center text-sm text-slate-500">Không có máy.</div>
      )}

      {filtered.length > 0 && (
        <div className="max-h-96 overflow-auto">
          <ul className="divide-y divide-slate-100">
            {filtered.map((m) => {
              const checked = selectedIds.includes(m.id);
              return (
                <li
                  key={m.id}
                  onClick={() => onToggle(m.id)}
                  className={`flex cursor-pointer items-center gap-3 px-4 py-2 hover:bg-slate-50 ${
                    checked ? "bg-slate-50" : ""
                  }`}
                >
                  <input type="checkbox" readOnly checked={checked} />
                  <span
                    className={`h-1.5 w-1.5 rounded-full ${
                      m.is_online ? "bg-emerald-500" : "bg-slate-400"
                    }`}
                  />
                  <span className="flex-1 text-sm">
                    <span className="font-medium">{m.employee_name}</span>
                    <span className="text-slate-500"> · {m.employee_email}</span>
                  </span>
                  <span className="font-mono text-xs text-slate-500">{m.hostname ?? "—"}</span>
                </li>
              );
            })}
          </ul>
        </div>
      )}
    </div>
  );
}

function ResultsPanel({ commandIds }: { commandIds: string[] }) {
  const queries = useQueries({
    queries: commandIds.map((id) => ({
      queryKey: ["command", id],
      queryFn: () => getCommand(id),
      refetchInterval: (q: { state: { data?: Command | undefined } }) => {
        const data = q.state.data;
        if (!data) return 2000;
        const final = ["completed", "failed", "timeout", "cancelled"];
        return final.includes(data.status) ? false : 2000;
      },
    })),
  });

  return (
    <div className="rounded-lg border bg-white shadow-sm">
      <div className="border-b p-4">
        <h3 className="text-base font-medium">Kết quả ({commandIds.length})</h3>
      </div>
      <ul className="divide-y divide-slate-100">
        {queries.map((q, i) => {
          const id = commandIds[i];
          if (!id) return null;
          if (q.isLoading || !q.data) {
            return (
              <li key={id} className="p-4 text-sm text-slate-500">
                <code className="text-xs">{id}</code> — đang chờ kết quả...
              </li>
            );
          }
          return <ResultRow key={id} command={q.data} />;
        })}
      </ul>
    </div>
  );
}

function ResultRow({ command }: { command: Command }) {
  const [expanded, setExpanded] = useState(false);
  const final = ["completed", "failed", "timeout", "cancelled"].includes(command.status);

  const statusStyle = {
    completed: "bg-emerald-50 text-emerald-700",
    failed: "bg-red-50 text-red-700",
    timeout: "bg-amber-50 text-amber-700",
    cancelled: "bg-slate-100 text-slate-500",
    pending: "bg-slate-100 text-slate-500",
    dispatched: "bg-blue-50 text-blue-700",
    running: "bg-blue-50 text-blue-700",
  }[command.status];

  return (
    <li className="p-4">
      <button
        type="button"
        className="flex w-full items-center justify-between gap-3 text-left"
        onClick={() => setExpanded((v) => !v)}
      >
        <div className="flex items-center gap-3">
          <span className={`rounded-full px-2 py-0.5 text-xs ${statusStyle}`}>
            {command.status}
          </span>
          <code className="text-xs text-slate-500">{command.machine_id.slice(0, 8)}</code>
          {command.exit_code !== null && command.exit_code !== undefined && (
            <span className="text-xs text-slate-500">exit {command.exit_code}</span>
          )}
        </div>
        {final && (
          <span className="text-xs text-slate-400">{expanded ? "Ẩn" : "Xem output"}</span>
        )}
      </button>

      {expanded && command.stdout && (
        <pre className="mt-3 max-h-64 overflow-auto rounded-md bg-slate-900 p-3 text-xs text-slate-100">
{command.stdout}
        </pre>
      )}
      {expanded && command.stderr && (
        <pre className="mt-2 max-h-64 overflow-auto rounded-md border border-red-200 bg-red-50 p-3 text-xs text-red-900">
{command.stderr}
        </pre>
      )}
    </li>
  );
}
