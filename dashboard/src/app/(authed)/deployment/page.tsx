"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { ConfirmDialog } from "@/components/ConfirmDialog";
import { APIError } from "@/lib/api-client";
import {
  activateDeploymentToken,
  createDeploymentToken,
  type DeploymentToken,
  listDeploymentTokens,
  revokeDeploymentToken,
} from "@/lib/queries";

// Deployment tokens replace the per-employee onboarding-code flow when
// rolling out to many machines: admin publishes ONE token and all
// employees install with the same setup.exe + their own email.

export default function DeploymentPage() {
  const queryClient = useQueryClient();
  const [includeRevoked, setIncludeRevoked] = useState(false);
  const [revoking, setRevoking] = useState<DeploymentToken | null>(null);

  const tokensQuery = useQuery({
    queryKey: ["deployment-tokens", { includeRevoked }],
    queryFn: () => listDeploymentTokens(includeRevoked),
  });

  const revokeMutation = useMutation({
    mutationFn: (id: string) => revokeDeploymentToken(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["deployment-tokens"] });
      setRevoking(null);
    },
  });

  const activateMutation = useMutation({
    mutationFn: (id: string) => activateDeploymentToken(id),
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: ["deployment-tokens"] }),
  });

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-2xl font-semibold tracking-tight">Deployment</h2>
        <p className="text-sm text-slate-500">
          Tạo 1 token, gửi cùng 1 link cài đặt cho hàng nghìn nhân viên. Mỗi nhân viên chỉ
          cần nhập email khi chạy installer — máy được tự đăng ký.
        </p>
      </div>

      <CreateTokenForm onCreated={() => tokensQuery.refetch()} />

      <div className="rounded-lg border bg-white shadow-sm">
        <div className="flex items-center justify-between border-b p-4">
          <h3 className="text-sm font-medium">Token deployment</h3>
          <label className="flex items-center gap-2 text-sm text-slate-600">
            <input
              type="checkbox"
              checked={includeRevoked}
              onChange={(e) => setIncludeRevoked(e.target.checked)}
            />
            Hiện cả token đã thu hồi / hết hạn
          </label>
        </div>

        {tokensQuery.isLoading && (
          <div className="p-8 text-center text-sm text-slate-500">Đang tải...</div>
        )}

        {tokensQuery.data && tokensQuery.data.items.length === 0 && (
          <div className="p-8 text-center text-sm text-slate-500">
            Chưa có token deployment. Tạo token đầu tiên ở form phía trên.
          </div>
        )}

        {tokensQuery.data && tokensQuery.data.items.length > 0 && (
          <ul className="divide-y divide-slate-100">
            {tokensQuery.data.items.map((t) => (
              <TokenRow
                key={t.id}
                token={t}
                onRevoke={() => setRevoking(t)}
                onActivate={() => activateMutation.mutate(t.id)}
                activating={activateMutation.isPending}
              />
            ))}
          </ul>
        )}
      </div>

      <ConfirmDialog
        open={revoking !== null}
        tone="danger"
        title="Thu hồi token này?"
        description={
          <div className="space-y-2">
            <p>
              Token <code className="font-mono">{revoking?.code}</code> sẽ bị vô hiệu hóa.
              Các máy đã enroll trước đó vẫn hoạt động bình thường — chỉ cài đặt mới bị từ chối.
            </p>
            <p className="text-xs text-slate-500">
              Không thể hoàn tác. Tạo token mới nếu cần tiếp tục cài thêm máy.
            </p>
          </div>
        }
        confirmLabel="Thu hồi"
        cancelLabel="Hủy"
        isPending={revokeMutation.isPending}
        onCancel={() => setRevoking(null)}
        onConfirm={() => {
          if (revoking) revokeMutation.mutate(revoking.id);
        }}
      />
    </div>
  );
}

function CreateTokenForm({ onCreated }: { onCreated: () => void }) {
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [code, setCode] = useState("");
  const [ttlDays, setTtlDays] = useState(365);
  const [maxUses, setMaxUses] = useState("");
  const [allowedDomains, setAllowedDomains] = useState("");
  const [requireEmail, setRequireEmail] = useState(false);
  const [setActive, setSetActive] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [createdCode, setCreatedCode] = useState<string | null>(null);

  const mutation = useMutation({
    mutationFn: createDeploymentToken,
    onSuccess: (data) => {
      setCreatedCode(data.code);
      setName("");
      setDescription("");
      setCode("");
      setMaxUses("");
      setAllowedDomains("");
      setError(null);
      onCreated();
    },
    onError: (err) => {
      setError(err instanceof APIError ? err.message : "Tạo token thất bại");
    },
  });

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setCreatedCode(null);

    const domains = allowedDomains
      .split(",")
      .map((d) => d.trim().toLowerCase())
      .filter(Boolean);

    const maxUsesNum = maxUses ? Number(maxUses) : undefined;

    mutation.mutate({
      name,
      description: description || undefined,
      code: code.trim() ? code.trim() : undefined,
      ttl_days: ttlDays,
      max_uses: maxUsesNum && maxUsesNum > 0 ? maxUsesNum : undefined,
      allowed_email_domains: domains.length > 0 ? domains : undefined,
      require_email: requireEmail,
      set_active: setActive,
    });
  }

  return (
    <div className="rounded-lg border bg-white p-6 shadow-sm">
      <h3 className="text-base font-medium">Tạo token deployment</h3>
      <p className="mt-1 text-xs text-slate-500">
        Tạo 1 token, gửi cùng 1 link cho tất cả nhân viên. Mã có thể tự đặt (vd:{" "}
        <code className="rounded bg-slate-100 px-1">PLAY</code>) — dễ nhớ để lồng vào video
        đào tạo.
      </p>
      <form onSubmit={handleSubmit} className="mt-4 grid grid-cols-1 gap-4 md:grid-cols-2">
        <div>
          <label className="block text-xs font-medium text-slate-600">
            Tên (ví dụ: &quot;Q2 2026 rollout&quot;)
          </label>
          <input
            type="text"
            required
            value={name}
            onChange={(e) => setName(e.target.value)}
            className="mt-1 block w-full rounded-md border border-slate-200 px-3 py-2 text-sm"
          />
        </div>
        <div>
          <label className="block text-xs font-medium text-slate-600">
            Mã (tùy chọn — để trống sẽ tự sinh)
          </label>
          <input
            type="text"
            placeholder="PLAY"
            maxLength={32}
            value={code}
            onChange={(e) => setCode(e.target.value.toUpperCase())}
            pattern="[A-Z0-9_\-]*"
            className="mt-1 block w-full rounded-md border border-slate-200 px-3 py-2 font-mono text-sm uppercase"
          />
          <p className="mt-1 text-xs text-slate-500">
            2-32 ký tự: A-Z, 0-9, dấu gạch ngang. Không phân biệt hoa thường khi nhân viên gõ.
          </p>
        </div>
        <div className="md:col-span-2">
          <label className="block text-xs font-medium text-slate-600">Mô tả (tùy chọn)</label>
          <input
            type="text"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            className="mt-1 block w-full rounded-md border border-slate-200 px-3 py-2 text-sm"
          />
        </div>
        <div>
          <label className="block text-xs font-medium text-slate-600">Hiệu lực (ngày)</label>
          <input
            type="number"
            min={1}
            max={730}
            required
            value={ttlDays}
            onChange={(e) => setTtlDays(Number(e.target.value) || 365)}
            className="mt-1 block w-full rounded-md border border-slate-200 px-3 py-2 text-sm"
          />
        </div>
        <div>
          <label className="block text-xs font-medium text-slate-600">
            Giới hạn lượt dùng (để trống = không giới hạn)
          </label>
          <input
            type="number"
            min={1}
            value={maxUses}
            onChange={(e) => setMaxUses(e.target.value)}
            className="mt-1 block w-full rounded-md border border-slate-200 px-3 py-2 text-sm"
          />
        </div>
        <div className="md:col-span-2">
          <label className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={requireEmail}
              onChange={(e) => setRequireEmail(e.target.checked)}
            />
            Bắt buộc nhập email khi cài (mặc định: chỉ cần mã, định danh = Windows user @ hostname)
          </label>
        </div>
        {requireEmail && (
          <div className="md:col-span-2">
            <label className="block text-xs font-medium text-slate-600">
              Email domain được phép (comma-separated, để trống = mọi domain)
            </label>
            <input
              type="text"
              placeholder="smartxcore.com, xbflow.com"
              value={allowedDomains}
              onChange={(e) => setAllowedDomains(e.target.value)}
              className="mt-1 block w-full rounded-md border border-slate-200 px-3 py-2 text-sm"
            />
          </div>
        )}
        <div className="md:col-span-2">
          <label className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={setActive}
              onChange={(e) => setSetActive(e.target.checked)}
            />
            Đặt làm token đang hoạt động (active)
          </label>
        </div>
        <div className="md:col-span-2">
          {error && (
            <div className="mb-3 rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700">
              {error}
            </div>
          )}
          {createdCode && (
            <div className="mb-3 rounded-md border border-emerald-200 bg-emerald-50 px-3 py-2 text-sm">
              <p className="font-medium text-emerald-900">Token đã tạo:</p>
              <code className="mt-1 block text-base font-bold text-emerald-900">
                {createdCode}
              </code>
              <p className="mt-1 text-xs text-emerald-700">
                {setActive
                  ? `Đã active. Cho nhân viên xem mã trong video đào tạo, gửi link https://smartxcore.com/install — họ tải, mở, gõ "${createdCode}" → xong.`
                  : "Token đã tạo nhưng chưa active. Click Active ở danh sách bên dưới khi sẵn sàng."}
              </p>
            </div>
          )}
          <button
            type="submit"
            disabled={mutation.isPending}
            className="rounded-md bg-slate-900 px-5 py-2 text-sm font-medium text-white hover:bg-slate-800 disabled:opacity-50"
          >
            {mutation.isPending ? "Đang tạo..." : "Tạo token"}
          </button>
        </div>
      </form>
    </div>
  );
}

function TokenRow({
  token,
  onRevoke,
  onActivate,
  activating,
}: {
  token: DeploymentToken;
  onRevoke: () => void;
  onActivate: () => void;
  activating: boolean;
}) {
  const isExpired = new Date(token.expires_at) < new Date();
  const isExhausted = token.max_uses != null && token.current_uses >= token.max_uses;

  let statusLabel: string;
  let statusStyle: string;
  if (token.revoked_at) {
    statusLabel = "revoked";
    statusStyle = "bg-slate-100 text-slate-500";
  } else if (isExpired) {
    statusLabel = "expired";
    statusStyle = "bg-amber-50 text-amber-700";
  } else if (isExhausted) {
    statusLabel = "exhausted";
    statusStyle = "bg-amber-50 text-amber-700";
  } else if (token.is_active) {
    statusLabel = "active";
    statusStyle = "bg-emerald-50 text-emerald-700";
  } else {
    statusLabel = "inactive";
    statusStyle = "bg-slate-100 text-slate-500";
  }

  const usable = !token.revoked_at && !isExpired && !isExhausted;

  return (
    <li className="px-4 py-3">
      <div className="flex items-center justify-between gap-4">
        <div className="min-w-0 flex-1 space-y-1">
          <div className="flex items-center gap-3">
            <code className="rounded bg-slate-100 px-2 py-1 text-xs font-bold">
              {token.code}
            </code>
            <span className={`rounded-full px-2 py-0.5 text-xs ${statusStyle}`}>
              {statusLabel}
            </span>
            <span className="text-sm font-medium text-slate-900">{token.name}</span>
          </div>
          <p className="text-xs text-slate-500">
            Đã dùng {token.current_uses}
            {token.max_uses != null ? ` / ${token.max_uses}` : ""} lần · Hết hạn{" "}
            {new Date(token.expires_at).toLocaleString("vi-VN")}
            {token.allowed_email_domains && token.allowed_email_domains.length > 0
              ? ` · Domain: ${token.allowed_email_domains.join(", ")}`
              : ""}
          </p>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          {usable && !token.is_active && (
            <button
              type="button"
              disabled={activating}
              onClick={onActivate}
              className="rounded-md border border-emerald-200 bg-emerald-50 px-3 py-1 text-xs text-emerald-800 hover:bg-emerald-100 disabled:opacity-50"
            >
              Đặt active
            </button>
          )}
          {usable && (
            <button
              type="button"
              onClick={onRevoke}
              className="rounded-md border border-slate-200 px-3 py-1 text-xs text-slate-600 hover:border-red-200 hover:bg-red-50 hover:text-red-700"
            >
              Thu hồi
            </button>
          )}
        </div>
      </div>
    </li>
  );
}
