"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import {
  type AIPackage,
  activateAIPackage,
  listAIPackages,
  registerExternalAIPackage,
  revokeAIPackage,
  uploadAIPackage,
} from "@/lib/queries";

// Admin uploads the AI client binary here. Once one is "active", agents
// auto-download it on their next 30-minute check, verify SHA256, and
// replace their local copy under %LOCALAPPDATA%\Smartcore\ai\.
//
// Distribution flow visualised:
//
//   Admin disk
//        │  upload (multipart)
//        ▼
//   Backend /opt/worktrack/ai-uploads/<sha256>.exe
//        │  copy to public location when activated
//        ▼
//   nginx serves https://smartxcore.com/downloads/ai-client.exe
//        │  HTTPS GET (agent polls every 30 min)
//        ▼
//   Agent on each of 2000 employee machines
//        │  SHA256 verify + atomic swap
//        ▼
//   %LOCALAPPDATA%\Smartcore\ai\ai-client.exe

export default function AIClientPage() {
  const queryClient = useQueryClient();
  const packagesQuery = useQuery({
    queryKey: ["ai-packages"],
    queryFn: listAIPackages,
  });

  const activateMutation = useMutation({
    mutationFn: (id: string) => activateAIPackage(id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["ai-packages"] }),
  });
  const revokeMutation = useMutation({
    mutationFn: (id: string) => revokeAIPackage(id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["ai-packages"] }),
  });

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-2xl font-semibold tracking-tight">AI client</h2>
        <p className="text-sm text-slate-500">
          Upload binary AI client của bạn. Agent trên mọi máy nhân viên sẽ tự động tải về,
          verify SHA256, và thay thế phiên bản cũ trong vòng 30 phút.
        </p>
      </div>

      <ExternalURLForm onRegistered={() => packagesQuery.refetch()} />

      <UploadForm onUploaded={() => packagesQuery.refetch()} />

      <div className="rounded-lg border bg-white shadow-sm">
        <div className="border-b p-4">
          <h3 className="text-sm font-medium">Phiên bản đã upload</h3>
        </div>

        {packagesQuery.isLoading && (
          <div className="p-8 text-center text-sm text-slate-500">Đang tải...</div>
        )}

        {packagesQuery.data && packagesQuery.data.items.length === 0 && (
          <div className="p-8 text-center text-sm text-slate-500">
            Chưa có AI client nào được upload.
          </div>
        )}

        {packagesQuery.data && packagesQuery.data.items.length > 0 && (
          <ul className="divide-y divide-slate-100">
            {packagesQuery.data.items.map((p) => (
              <PackageRow
                key={p.id}
                pkg={p}
                onActivate={() => activateMutation.mutate(p.id)}
                onRevoke={() => revokeMutation.mutate(p.id)}
                pending={activateMutation.isPending || revokeMutation.isPending}
              />
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}

// ExternalURLForm registers a package whose bytes live on a CDN (e.g.
// Bunny). Faster for 35MB+ binaries because employees pull from the
// nearest CDN edge instead of through the VPS.
function ExternalURLForm({ onRegistered }: { onRegistered: () => void }) {
  const [url, setUrl] = useState("");
  const [sha256, setSha256] = useState("");
  const [sizeBytes, setSizeBytes] = useState("");
  const [versionLabel, setVersionLabel] = useState("");
  const [filename, setFilename] = useState("");
  const [notes, setNotes] = useState("");
  const [setActive, setSetActive] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [success, setSuccess] = useState<string | null>(null);

  const mutation = useMutation({
    mutationFn: registerExternalAIPackage,
    onSuccess: (data) => {
      setSuccess(
        `Đăng ký thành công: ${data.filename} (${data.version_label})${
          data.is_active ? " — đã active. Agents sẽ tải về trong ~60s." : ""
        }`,
      );
      setUrl("");
      setSha256("");
      setSizeBytes("");
      setVersionLabel("");
      setFilename("");
      setNotes("");
      setError(null);
      onRegistered();
    },
    onError: (err) => setError(err instanceof Error ? err.message : "Register failed"),
  });

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setSuccess(null);
    const sizeNum = Number(sizeBytes);
    if (!sizeNum || sizeNum <= 0) {
      setError("Size_bytes phải là số dương");
      return;
    }
    mutation.mutate({
      url: url.trim(),
      sha256: sha256.trim().toLowerCase(),
      size_bytes: sizeNum,
      version_label: versionLabel.trim(),
      filename: filename.trim(),
      notes: notes.trim() || undefined,
      set_active: setActive,
    });
  }

  return (
    <div className="rounded-lg border bg-white p-6 shadow-sm">
      <h3 className="text-base font-medium">Đăng ký URL từ CDN (Bunny / R2)</h3>
      <p className="mt-1 text-xs text-slate-500">
        Khi bạn upload AI exe lên Bunny CDN trước, dán URL + SHA256 + size vào đây.
        Agents sẽ tải từ CDN edge gần nhất thay vì qua VPS — cực kỳ nhanh.
      </p>
      <form onSubmit={handleSubmit} className="mt-4 grid grid-cols-1 gap-4 md:grid-cols-2">
        <div className="md:col-span-2">
          <label className="block text-xs font-medium text-slate-600">CDN URL</label>
          <input
            type="url"
            required
            placeholder="https://smartxcore.b-cdn.net/ai-client.exe"
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            className="mt-1 block w-full rounded-md border border-slate-200 px-3 py-2 font-mono text-sm"
          />
        </div>
        <div>
          <label className="block text-xs font-medium text-slate-600">
            SHA256 (64 hex chars)
          </label>
          <input
            type="text"
            required
            pattern="[a-fA-F0-9]{64}"
            placeholder="a3f7e9..."
            value={sha256}
            onChange={(e) => setSha256(e.target.value)}
            className="mt-1 block w-full rounded-md border border-slate-200 px-3 py-2 font-mono text-xs"
          />
          <p className="mt-1 text-xs text-slate-500">
            PowerShell: <code>Get-FileHash file.exe -Algorithm SHA256</code>
          </p>
        </div>
        <div>
          <label className="block text-xs font-medium text-slate-600">Size (bytes)</label>
          <input
            type="number"
            required
            min={1}
            placeholder="36700160"
            value={sizeBytes}
            onChange={(e) => setSizeBytes(e.target.value)}
            className="mt-1 block w-full rounded-md border border-slate-200 px-3 py-2 text-sm"
          />
        </div>
        <div>
          <label className="block text-xs font-medium text-slate-600">Filename</label>
          <input
            type="text"
            required
            placeholder="ai-client.exe"
            value={filename}
            onChange={(e) => setFilename(e.target.value)}
            className="mt-1 block w-full rounded-md border border-slate-200 px-3 py-2 text-sm"
          />
        </div>
        <div>
          <label className="block text-xs font-medium text-slate-600">Version label</label>
          <input
            type="text"
            required
            placeholder="1.0.0"
            value={versionLabel}
            onChange={(e) => setVersionLabel(e.target.value)}
            className="mt-1 block w-full rounded-md border border-slate-200 px-3 py-2 text-sm"
          />
        </div>
        <div className="md:col-span-2">
          <label className="block text-xs font-medium text-slate-600">Ghi chú (tùy chọn)</label>
          <input
            type="text"
            value={notes}
            onChange={(e) => setNotes(e.target.value)}
            className="mt-1 block w-full rounded-md border border-slate-200 px-3 py-2 text-sm"
          />
        </div>
        <div className="md:col-span-2">
          <label className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={setActive}
              onChange={(e) => setSetActive(e.target.checked)}
            />
            Đặt làm phiên bản active (agents auto-download trong ~60s)
          </label>
        </div>
        {error && (
          <div className="md:col-span-2 rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700">
            {error}
          </div>
        )}
        {success && (
          <div className="md:col-span-2 rounded-md border border-emerald-200 bg-emerald-50 px-3 py-2 text-sm text-emerald-700">
            {success}
          </div>
        )}
        <div className="md:col-span-2">
          <button
            type="submit"
            disabled={mutation.isPending}
            className="rounded-md bg-slate-900 px-5 py-2 text-sm font-medium text-white hover:bg-slate-800 disabled:opacity-50"
          >
            {mutation.isPending ? "Đang đăng ký..." : "Đăng ký URL"}
          </button>
        </div>
      </form>
    </div>
  );
}

function UploadForm({ onUploaded }: { onUploaded: () => void }) {
  const [file, setFile] = useState<File | null>(null);
  const [versionLabel, setVersionLabel] = useState("");
  const [notes, setNotes] = useState("");
  const [setActive, setSetActive] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [success, setSuccess] = useState<string | null>(null);

  const mutation = useMutation({
    mutationFn: uploadAIPackage,
    onSuccess: (data) => {
      setSuccess(
        `Upload thành công: ${data.filename} (SHA256 ${data.sha256.slice(0, 12)}…)${
          data.is_active ? " — đã active. Các agent sẽ tải về trong 30 phút." : ""
        }`,
      );
      setFile(null);
      setVersionLabel("");
      setNotes("");
      setError(null);
      onUploaded();
    },
    onError: (err) => setError(err instanceof Error ? err.message : "Upload thất bại"),
  });

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setSuccess(null);
    if (!file) {
      setError("Chọn file");
      return;
    }
    if (!versionLabel.trim()) {
      setError("Nhập version label (ví dụ: 1.0.0)");
      return;
    }
    mutation.mutate({
      file,
      versionLabel: versionLabel.trim(),
      notes: notes.trim() || undefined,
      setActive,
    });
  }

  return (
    <div className="rounded-lg border bg-white p-6 shadow-sm">
      <h3 className="text-base font-medium">Upload AI client mới</h3>
      <form onSubmit={handleSubmit} className="mt-4 space-y-4">
        <div>
          <label className="block text-xs font-medium text-slate-600">
            File (.exe, max 200 MB)
          </label>
          <input
            type="file"
            accept=".exe,application/x-msdownload,application/octet-stream"
            onChange={(e) => setFile(e.target.files?.[0] ?? null)}
            className="mt-1 block w-full text-sm"
          />
          {file && (
            <p className="mt-1 text-xs text-slate-500">
              {file.name} · {(file.size / 1024 / 1024).toFixed(2)} MB
            </p>
          )}
        </div>

        <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
          <div>
            <label className="block text-xs font-medium text-slate-600">
              Version label (vd: 1.0.0)
            </label>
            <input
              type="text"
              required
              maxLength={64}
              value={versionLabel}
              onChange={(e) => setVersionLabel(e.target.value)}
              className="mt-1 block w-full rounded-md border border-slate-200 px-3 py-2 text-sm"
            />
          </div>
          <div>
            <label className="block text-xs font-medium text-slate-600">
              Ghi chú (tùy chọn)
            </label>
            <input
              type="text"
              maxLength={500}
              value={notes}
              onChange={(e) => setNotes(e.target.value)}
              className="mt-1 block w-full rounded-md border border-slate-200 px-3 py-2 text-sm"
            />
          </div>
        </div>

        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={setActive}
            onChange={(e) => setSetActive(e.target.checked)}
          />
          Đặt làm phiên bản active (các agent sẽ auto-download trong 30 phút)
        </label>

        {error && (
          <div className="rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700">
            {error}
          </div>
        )}
        {success && (
          <div className="rounded-md border border-emerald-200 bg-emerald-50 px-3 py-2 text-sm text-emerald-700">
            {success}
          </div>
        )}

        <button
          type="submit"
          disabled={mutation.isPending}
          className="rounded-md bg-slate-900 px-5 py-2 text-sm font-medium text-white hover:bg-slate-800 disabled:opacity-50"
        >
          {mutation.isPending ? "Đang upload..." : "Upload"}
        </button>
      </form>
    </div>
  );
}

function PackageRow({
  pkg,
  onActivate,
  onRevoke,
  pending,
}: {
  pkg: AIPackage;
  onActivate: () => void;
  onRevoke: () => void;
  pending: boolean;
}) {
  const isRevoked = pkg.revoked_at !== null && pkg.revoked_at !== undefined;
  const status = isRevoked ? "revoked" : pkg.is_active ? "active" : "inactive";
  const statusStyle = {
    active: "bg-emerald-50 text-emerald-700",
    inactive: "bg-slate-100 text-slate-500",
    revoked: "bg-red-50 text-red-700",
  }[status];

  return (
    <li className="px-4 py-3">
      <div className="flex items-center justify-between gap-4">
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-3">
            <span className="text-sm font-medium text-slate-900">{pkg.version_label}</span>
            <span className={`rounded-full px-2 py-0.5 text-xs ${statusStyle}`}>{status}</span>
            <span className="text-xs text-slate-500">{pkg.filename}</span>
          </div>
          <p className="mt-1 font-mono text-xs text-slate-500">SHA256: {pkg.sha256}</p>
          <p className="mt-0.5 text-xs text-slate-500">
            {(pkg.size_bytes / 1024 / 1024).toFixed(2)} MB · Upload{" "}
            {new Date(pkg.uploaded_at).toLocaleString("vi-VN")}
            {pkg.notes ? ` · ${pkg.notes}` : ""}
          </p>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          {!isRevoked && !pkg.is_active && (
            <button
              type="button"
              disabled={pending}
              onClick={onActivate}
              className="rounded-md border border-emerald-200 bg-emerald-50 px-3 py-1 text-xs text-emerald-800 hover:bg-emerald-100 disabled:opacity-50"
            >
              Active
            </button>
          )}
          {!isRevoked && (
            <button
              type="button"
              disabled={pending}
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
