"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { APIError } from "@/lib/api-client";
import { createOnboardingToken, listOnboardingTokens, type OnboardingToken } from "@/lib/queries";

export default function OnboardingPage() {
  const queryClient = useQueryClient();
  const [includeUsed, setIncludeUsed] = useState(false);

  const tokensQuery = useQuery({
    queryKey: ["onboarding-tokens", { includeUsed }],
    queryFn: () => listOnboardingTokens(includeUsed),
  });

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-2xl font-semibold tracking-tight">Onboarding nhân viên</h2>
        <p className="text-sm text-slate-500">
          Tạo mã 1 lần để nhân viên cài đặt agent. Mã có hiệu lực 72 giờ mặc định.
        </p>
      </div>

      <CreateTokenForm
        onCreated={() => queryClient.invalidateQueries({ queryKey: ["onboarding-tokens"] })}
      />

      <div className="rounded-lg border bg-white shadow-sm">
        <div className="flex items-center justify-between border-b p-4">
          <h3 className="text-sm font-medium">Danh sách mã</h3>
          <label className="flex items-center gap-2 text-sm text-slate-600">
            <input
              type="checkbox"
              checked={includeUsed}
              onChange={(e) => setIncludeUsed(e.target.checked)}
            />
            Hiện mã đã dùng / hết hạn
          </label>
        </div>

        {tokensQuery.isLoading && (
          <div className="p-8 text-center text-sm text-slate-500">Đang tải...</div>
        )}

        {tokensQuery.data && tokensQuery.data.items.length === 0 && (
          <div className="p-8 text-center text-sm text-slate-500">Chưa có mã nào.</div>
        )}

        {tokensQuery.data && tokensQuery.data.items.length > 0 && (
          <ul className="divide-y divide-slate-100">
            {tokensQuery.data.items.map((t) => (
              <TokenRow key={t.id} token={t} />
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}

function CreateTokenForm({ onCreated }: { onCreated: () => void }) {
  const [employeeName, setEmployeeName] = useState("");
  const [employeeEmail, setEmployeeEmail] = useState("");
  const [department, setDepartment] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [createdCode, setCreatedCode] = useState<string | null>(null);

  const mutation = useMutation({
    mutationFn: createOnboardingToken,
    onSuccess: (data) => {
      setCreatedCode(data.code);
      setEmployeeName("");
      setEmployeeEmail("");
      setDepartment("");
      setError(null);
      onCreated();
    },
    onError: (err) => {
      setError(err instanceof APIError ? err.message : "Tạo mã thất bại");
    },
  });

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setCreatedCode(null);
    mutation.mutate({
      employee_name: employeeName,
      employee_email: employeeEmail,
      department: department || undefined,
    });
  }

  return (
    <div className="rounded-lg border bg-white p-6 shadow-sm">
      <h3 className="text-base font-medium">Tạo mã mới</h3>
      <form onSubmit={handleSubmit} className="mt-4 grid grid-cols-1 gap-4 md:grid-cols-3">
        <div>
          <label className="block text-xs font-medium text-slate-600">Tên nhân viên</label>
          <input
            type="text"
            required
            value={employeeName}
            onChange={(e) => setEmployeeName(e.target.value)}
            className="mt-1 block w-full rounded-md border border-slate-200 px-3 py-2 text-sm"
          />
        </div>
        <div>
          <label className="block text-xs font-medium text-slate-600">Email</label>
          <input
            type="email"
            required
            value={employeeEmail}
            onChange={(e) => setEmployeeEmail(e.target.value)}
            className="mt-1 block w-full rounded-md border border-slate-200 px-3 py-2 text-sm"
          />
        </div>
        <div>
          <label className="block text-xs font-medium text-slate-600">Bộ phận (tùy chọn)</label>
          <input
            type="text"
            value={department}
            onChange={(e) => setDepartment(e.target.value)}
            className="mt-1 block w-full rounded-md border border-slate-200 px-3 py-2 text-sm"
          />
        </div>
        <div className="md:col-span-3">
          {error && (
            <div className="mb-3 rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700">
              {error}
            </div>
          )}
          {createdCode && <CreatedTokenPanel code={createdCode} />}
          <button
            type="submit"
            disabled={mutation.isPending}
            className="rounded-md bg-slate-900 px-4 py-2 text-sm font-medium text-white hover:bg-slate-800 disabled:opacity-50"
          >
            {mutation.isPending ? "Đang tạo..." : "Tạo mã"}
          </button>
        </div>
      </form>
    </div>
  );
}

function CreatedTokenPanel({ code }: { code: string }) {
  const [copied, setCopied] = useState<"code" | "link" | null>(null);
  const installLink =
    typeof window === "undefined"
      ? `https://smartxcore.com/install?code=${encodeURIComponent(code)}`
      : `${window.location.origin}/install?code=${encodeURIComponent(code)}`;

  async function copy(value: string, kind: "code" | "link") {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(kind);
      setTimeout(() => setCopied(null), 2000);
    } catch {
      // ignore
    }
  }

  return (
    <div className="mb-3 space-y-3 rounded-md border border-emerald-200 bg-emerald-50 px-4 py-3 text-sm">
      <div>
        <p className="text-xs font-medium uppercase tracking-wider text-emerald-700">
          Mã onboarding
        </p>
        <div className="mt-1 flex items-center justify-between gap-2">
          <code className="text-lg font-bold text-emerald-900">{code}</code>
          <button
            type="button"
            onClick={() => copy(code, "code")}
            className="rounded-md border border-emerald-300 bg-white px-3 py-1 text-xs hover:bg-emerald-50"
          >
            {copied === "code" ? "Đã copy" : "Copy mã"}
          </button>
        </div>
      </div>

      <div>
        <p className="text-xs font-medium uppercase tracking-wider text-emerald-700">
          Link gửi nhân viên
        </p>
        <div className="mt-1 flex items-center justify-between gap-2">
          <code className="truncate text-xs text-emerald-900">{installLink}</code>
          <button
            type="button"
            onClick={() => copy(installLink, "link")}
            className="rounded-md border border-emerald-300 bg-white px-3 py-1 text-xs hover:bg-emerald-50"
          >
            {copied === "link" ? "Đã copy" : "Copy link"}
          </button>
        </div>
        <p className="mt-1 text-xs text-emerald-700">
          Gửi link này cho nhân viên. Họ click → tải installer → mã đã có sẵn.
        </p>
      </div>
    </div>
  );
}

function TokenRow({ token }: { token: OnboardingToken }) {
  const isUsed = token.used_at !== null && token.used_at !== undefined;
  const isExpired = new Date(token.expires_at) < new Date();
  const status = isUsed ? "used" : isExpired ? "expired" : "active";

  const statusStyle = {
    active: "bg-emerald-50 text-emerald-700",
    used: "bg-slate-100 text-slate-500",
    expired: "bg-amber-50 text-amber-700",
  }[status];

  return (
    <li className="px-4 py-3">
      <div className="flex items-center justify-between gap-4">
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-3">
            <code className="rounded bg-slate-100 px-2 py-1 text-xs font-bold">{token.code}</code>
            <span className={`rounded-full px-2 py-0.5 text-xs ${statusStyle}`}>{status}</span>
          </div>
          <p className="mt-1 text-sm text-slate-700">
            {token.employee_name} · {token.employee_email}
            {token.department ? ` · ${token.department}` : ""}
          </p>
          <p className="mt-0.5 text-xs text-slate-500">
            Tạo {new Date(token.created_at).toLocaleString("vi-VN")} · Hết hạn{" "}
            {new Date(token.expires_at).toLocaleString("vi-VN")}
          </p>
        </div>
      </div>
    </li>
  );
}
