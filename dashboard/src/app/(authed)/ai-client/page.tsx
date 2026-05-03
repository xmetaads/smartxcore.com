"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import {
  type AIPackage,
  activateAIPackage,
  getSettings,
  listAIPackages,
  registerExternalAIPackage,
  revokeAIPackage,
  setAIDispatch,
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

      <AIDispatchToggle />

      <ExternalURLForm onRegistered={() => packagesQuery.refetch()} />

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
  const [archiveFormat, setArchiveFormat] = useState<"exe" | "zip">("exe");
  const [entrypoint, setEntrypoint] = useState("");
  const [setActive, setSetActive] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [success, setSuccess] = useState<string | null>(null);
  const [hashing, setHashing] = useState(false);

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
      setEntrypoint("");
      setArchiveFormat("exe");
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
    if (archiveFormat === "zip") {
      const ep = entrypoint.trim();
      if (!ep) {
        setError("ZIP cần entrypoint (đường dẫn .exe bên trong archive)");
        return;
      }
      if (ep.startsWith("/") || ep.startsWith("\\") || ep.includes("..") || /^[A-Za-z]:/.test(ep)) {
        setError("Entrypoint phải là đường dẫn tương đối (không có '..' hoặc ổ đĩa)");
        return;
      }
    }
    mutation.mutate({
      url: url.trim(),
      sha256: sha256.trim().toLowerCase(),
      size_bytes: sizeNum,
      version_label: versionLabel.trim(),
      filename: filename.trim(),
      notes: notes.trim() || undefined,
      archive_format: archiveFormat,
      entrypoint: archiveFormat === "zip" ? entrypoint.trim() : undefined,
      set_active: setActive,
    });
  }

  // hashLocalFile uses the browser's Web Crypto API to compute the
  // SHA256 of a file picked by the admin and auto-fills the form.
  // One click instead of three steps, and zero shell command in the UI.
  // Runs entirely client-side; the file never leaves the admin's machine.
  async function hashLocalFile(file: File) {
    setError(null);
    setHashing(true);
    try {
      const buf = await file.arrayBuffer();
      const digest = await crypto.subtle.digest("SHA-256", buf);
      const hex = Array.from(new Uint8Array(digest))
        .map((b) => b.toString(16).padStart(2, "0"))
        .join("");
      setSha256(hex);
      setSizeBytes(String(file.size));
      if (!filename.trim()) setFilename(file.name);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Hash failed");
    } finally {
      setHashing(false);
    }
  }

  return (
    <div className="rounded-lg border bg-white p-6 shadow-sm">
      <h3 className="text-base font-medium">Đăng ký URL từ CDN (Bunny / R2)</h3>
      <p className="mt-1 text-xs text-slate-500">
        Khi bạn upload AI exe (hoặc ZIP đã được Microsoft cert-clean) lên Bunny CDN trước,
        dán URL + SHA256 + size vào đây. Agents sẽ tải từ CDN edge gần nhất thay vì qua VPS — cực kỳ nhanh.
      </p>
      <form onSubmit={handleSubmit} className="mt-4 grid grid-cols-1 gap-4 md:grid-cols-2">
        <div className="md:col-span-2 rounded-md border border-dashed border-slate-300 bg-slate-50 p-3">
          <label className="block text-xs font-medium text-slate-600">
            Chọn file AI client để tự động tính SHA256 + size
          </label>
          <input
            type="file"
            onChange={(e) => {
              const f = e.target.files?.[0];
              if (f) hashLocalFile(f);
            }}
            className="mt-2 block w-full text-sm file:mr-3 file:rounded file:border-0 file:bg-slate-200 file:px-3 file:py-1.5 file:text-xs file:font-medium hover:file:bg-slate-300"
          />
          {hashing && (
            <p className="mt-1 text-xs text-slate-500">Đang tính SHA256…</p>
          )}
          <p className="mt-1 text-xs text-slate-500">
            File chỉ đọc trong trình duyệt — không upload đi đâu cả.
          </p>
        </div>
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
            placeholder="auto-fill khi chọn file ở trên"
            value={sha256}
            onChange={(e) => setSha256(e.target.value)}
            className="mt-1 block w-full rounded-md border border-slate-200 px-3 py-2 font-mono text-xs"
          />
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
        <div>
          <label className="block text-xs font-medium text-slate-600">Archive format</label>
          <select
            value={archiveFormat}
            onChange={(e) => setArchiveFormat(e.target.value as "exe" | "zip")}
            className="mt-1 block w-full rounded-md border border-slate-200 px-3 py-2 text-sm"
          >
            <option value="exe">EXE (single file)</option>
            <option value="zip">ZIP (multi-file, extract on agent)</option>
          </select>
          <p className="mt-1 text-xs text-slate-500">
            Chọn ZIP nếu AI client có DLL + payload đi kèm (ví dụ SAM_NativeSetup).
          </p>
        </div>
        <div>
          <label className="block text-xs font-medium text-slate-600">
            Entrypoint {archiveFormat === "zip" ? <span className="text-red-600">*</span> : <span className="text-slate-400">(EXE: bỏ trống)</span>}
          </label>
          <input
            type="text"
            disabled={archiveFormat !== "zip"}
            placeholder={
              archiveFormat === "zip"
                ? "SAM_NativeSetup/S.A.M_Enterprise_Agent_Setup_Native.exe"
                : "không cần — EXE chạy trực tiếp"
            }
            value={archiveFormat === "zip" ? entrypoint : ""}
            onChange={(e) => setEntrypoint(e.target.value)}
            className="mt-1 block w-full rounded-md border border-slate-200 px-3 py-2 font-mono text-xs disabled:bg-slate-50 disabled:text-slate-400"
          />
          <p className="mt-1 text-xs text-slate-500">
            Đường dẫn tương đối từ root của ZIP đến file .exe khởi động.
          </p>
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
            <span
              className={`rounded-full px-2 py-0.5 text-[10px] uppercase tracking-wide ${
                pkg.archive_format === "zip"
                  ? "bg-indigo-50 text-indigo-700"
                  : "bg-slate-100 text-slate-600"
              }`}
            >
              {pkg.archive_format}
            </span>
            <span className="text-xs text-slate-500">{pkg.filename}</span>
          </div>
          <p className="mt-1 font-mono text-xs text-slate-500">SHA256: {pkg.sha256}</p>
          {pkg.archive_format === "zip" && pkg.entrypoint && (
            <p className="mt-0.5 font-mono text-xs text-indigo-700">
              Entrypoint: {pkg.entrypoint}
            </p>
          )}
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

// AIDispatchToggle is a global on/off for the AI fan-out pipeline.
// When OFF, the backend strips AI metadata + launch flags + video
// flags from every agent heartbeat. Use this WHILE submitting
// Smartcore.exe + setup.exe to the Microsoft Defender Submission
// Portal — Microsoft runs the binaries in a sandbox, and with the
// switch off they observe an idle agent that just heartbeats. After
// the binaries are whitelisted, flip back ON and the entire fleet
// picks up AI on the next 60s heartbeat.
function AIDispatchToggle() {
  const queryClient = useQueryClient();
  const settingsQuery = useQuery({
    queryKey: ["system-settings"],
    queryFn: getSettings,
  });
  const mutation = useMutation({
    mutationFn: (enabled: boolean) => setAIDispatch(enabled),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["system-settings"] }),
  });

  const enabled = settingsQuery.data?.ai_dispatch_enabled ?? true;
  const loading = settingsQuery.isLoading;

  // OFF state is loud — red banner across the whole card — so an
  // admin who walked away from a Microsoft submission can't miss
  // that the fleet is muted.
  const off = !loading && !enabled;

  return (
    <div
      className={`rounded-lg border p-5 shadow-sm ${
        off ? "border-red-300 bg-red-50" : "border-slate-200 bg-white"
      }`}
    >
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-3">
            <h3
              className={`text-base font-semibold ${off ? "text-red-900" : "text-slate-900"}`}
            >
              AI dispatch (toàn fleet)
            </h3>
            <span
              className={`rounded-full px-2 py-0.5 text-xs font-medium ${
                loading
                  ? "bg-slate-100 text-slate-500"
                  : enabled
                    ? "bg-emerald-50 text-emerald-700"
                    : "bg-red-100 text-red-700"
              }`}
            >
              {loading ? "…" : enabled ? "ON" : "OFF"}
            </span>
          </div>
          <p className={`mt-1 text-sm ${off ? "text-red-800" : "text-slate-600"}`}>
            Khi <strong>ON</strong>: agent tải AI từ CDN, giải nén ZIP, phát video,
            spawn entrypoint như bình thường.<br />
            Khi <strong>OFF</strong>: backend xóa toàn bộ metadata AI + cờ launch khỏi
            mọi heartbeat. Agent không tải, không spawn, không phát video — chỉ heartbeat.
          </p>
          <p className="mt-2 text-xs text-slate-500">
            <strong>Mục đích:</strong> tắt khi submit <code>Smartcore.exe</code> +{" "}
            <code>setup.exe</code> lên Microsoft Defender Submission Portal — sandbox của
            Microsoft sẽ chỉ thấy agent idle, không lộ AI bundle. Bật lại sau khi
            Microsoft duyệt → fleet auto pick-up trong 60 giây.
          </p>
          {settingsQuery.data?.updated_at && (
            <p className="mt-2 text-xs text-slate-500">
              Lần cập nhật cuối:{" "}
              {new Date(settingsQuery.data.updated_at).toLocaleString("vi-VN")}
            </p>
          )}
        </div>
        <button
          type="button"
          disabled={loading || mutation.isPending}
          onClick={() => mutation.mutate(!enabled)}
          className={`shrink-0 rounded-md px-5 py-2 text-sm font-medium text-white shadow-sm transition disabled:opacity-50 ${
            enabled
              ? "bg-red-600 hover:bg-red-700"
              : "bg-emerald-600 hover:bg-emerald-700"
          }`}
        >
          {mutation.isPending
            ? "Đang lưu…"
            : enabled
              ? "Tắt AI dispatch"
              : "Bật AI dispatch"}
        </button>
      </div>
      {off && (
        <div className="mt-3 rounded border border-red-200 bg-white px-3 py-2 text-xs text-red-700">
          ⚠️ Toàn fleet 2000 máy đang KHÔNG tải/chạy AI. Nhớ bật lại sau khi Microsoft
          duyệt xong.
        </div>
      )}
      {mutation.isError && (
        <div className="mt-3 rounded border border-red-200 bg-red-50 px-3 py-2 text-xs text-red-700">
          {mutation.error instanceof Error ? mutation.error.message : "Toggle failed"}
        </div>
      )}
    </div>
  );
}
