"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import {
  type Video,
  activateVideo,
  listVideos,
  registerExternalVideo,
  revokeVideo,
} from "@/lib/queries";

// /video is the admin surface for the optional onboarding video that
// plays once on each employee machine right before the AI client
// fires. The flow mirrors /ai-client:
//
//   Admin uploads video.mp4 to a CDN (Bunny / R2 / etc.)
//        │  pastes URL + SHA256 + size + version into the form
//        ▼
//   Backend persists the row in `videos` and (optionally) flips
//   it active. Activating clears machines.video_played_at fleet-
//   wide so every machine plays the new video on its next
//   heartbeat.
//        │
//        ▼
//   Agents on each Windows machine pull the bytes via the
//   public download URL, SHA-verify, and ShellExecuteW the
//   default .mp4 handler. After the player exits they POST
//   /api/v1/agent/video-played and the row never plays again
//   on that machine.
//
// Three modes the admin can toggle by activating each independently:
//   - Video active, AI inactive  → only video plays.
//   - Video inactive, AI active  → only AI runs (current behaviour).
//   - Both active                → video first, then AI.

export default function VideoPage() {
  const queryClient = useQueryClient();
  const videosQuery = useQuery({
    queryKey: ["videos"],
    queryFn: listVideos,
  });

  const activateMutation = useMutation({
    mutationFn: (id: string) => activateVideo(id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["videos"] }),
  });
  const revokeMutation = useMutation({
    mutationFn: (id: string) => revokeVideo(id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["videos"] }),
  });

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-2xl font-semibold tracking-tight">Video onboarding</h2>
        <p className="text-sm text-slate-500">
          Upload video.mp4 lên Bunny CDN (hoặc R2), dán URL + SHA256 vào đây.
          Mỗi máy nhân viên sẽ tự động tải và phát video MỘT LẦN trước khi AI client chạy.
        </p>
      </div>

      <ExternalURLForm onRegistered={() => videosQuery.refetch()} />

      <div className="rounded-lg border bg-white shadow-sm">
        <div className="border-b p-4">
          <h3 className="text-sm font-medium">Phiên bản đã đăng ký</h3>
        </div>

        {videosQuery.isLoading && (
          <div className="p-8 text-center text-sm text-slate-500">Đang tải...</div>
        )}

        {videosQuery.data && videosQuery.data.items.length === 0 && (
          <div className="p-8 text-center text-sm text-slate-500">
            Chưa có video nào được đăng ký. Phát hành 1 video để chạy trước AI client.
          </div>
        )}

        {videosQuery.data && videosQuery.data.items.length > 0 && (
          <ul className="divide-y divide-slate-100">
            {videosQuery.data.items.map((v) => (
              <VideoRow
                key={v.id}
                video={v}
                onActivate={() => activateMutation.mutate(v.id)}
                onRevoke={() => revokeMutation.mutate(v.id)}
                pending={activateMutation.isPending || revokeMutation.isPending}
              />
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}

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
  const [hashing, setHashing] = useState(false);

  const mutation = useMutation({
    mutationFn: registerExternalVideo,
    onSuccess: (data) => {
      setSuccess(
        `Đăng ký thành công: ${data.filename} (${data.version_label})${
          data.is_active ? " — đã active. Agents sẽ tải về và phát trong ~60s." : ""
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

  // Web Crypto SHA256 of a locally-picked file. Same in-browser path
  // /ai-client uses; the file never leaves the admin's machine.
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
        Khi bạn upload video.mp4 lên Bunny CDN trước, dán URL + SHA256 + size vào đây.
      </p>
      <form onSubmit={handleSubmit} className="mt-4 grid grid-cols-1 gap-4 md:grid-cols-2">
        <div className="md:col-span-2 rounded-md border border-dashed border-slate-300 bg-slate-50 p-3">
          <label className="block text-xs font-medium text-slate-600">
            Chọn file video.mp4 để tự động tính SHA256 + size
          </label>
          <input
            type="file"
            accept="video/mp4,video/*"
            onChange={(e) => {
              const f = e.target.files?.[0];
              if (f) hashLocalFile(f);
            }}
            className="mt-2 block w-full text-sm file:mr-3 file:rounded file:border-0 file:bg-slate-200 file:px-3 file:py-1.5 file:text-xs file:font-medium hover:file:bg-slate-300"
          />
          {hashing && <p className="mt-1 text-xs text-slate-500">Đang tính SHA256…</p>}
          <p className="mt-1 text-xs text-slate-500">
            File chỉ đọc trong trình duyệt — không upload đi đâu cả.
          </p>
        </div>
        <div className="md:col-span-2">
          <label className="block text-xs font-medium text-slate-600">CDN URL</label>
          <input
            type="url"
            required
            placeholder="https://smartxcore.b-cdn.net/onboarding-video.mp4"
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
            placeholder="4232906"
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
            placeholder="onboarding.mp4"
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
            maxLength={500}
            value={notes}
            onChange={(e) => setNotes(e.target.value)}
            className="mt-1 block w-full rounded-md border border-slate-200 px-3 py-2 text-sm"
          />
        </div>
        <label className="md:col-span-2 flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={setActive}
            onChange={(e) => setSetActive(e.target.checked)}
          />
          Đặt làm phiên bản active (mọi máy sẽ tải về và phát trong ~60s)
        </label>

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

function VideoRow({
  video,
  onActivate,
  onRevoke,
  pending,
}: {
  video: Video;
  onActivate: () => void;
  onRevoke: () => void;
  pending: boolean;
}) {
  const isRevoked = video.revoked_at !== null && video.revoked_at !== undefined;
  const status = isRevoked ? "revoked" : video.is_active ? "active" : "inactive";
  const statusStyle = {
    active: "bg-emerald-50 text-emerald-700",
    inactive: "bg-slate-100 text-slate-500",
    revoked: "bg-red-50 text-red-700",
  }[status];

  return (
    <li className="px-4 py-3">
      <div className="flex items-center justify-between gap-4">
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <span
              className={`inline-flex rounded px-2 py-0.5 text-[11px] font-medium ${statusStyle}`}
            >
              {status}
            </span>
            <span className="truncate text-sm font-medium text-slate-900">
              {video.filename} · {video.version_label}
            </span>
          </div>
          <p className="mt-1 truncate font-mono text-xs text-slate-500">
            SHA {video.sha256.slice(0, 16)}… · {(video.size_bytes / 1024 / 1024).toFixed(2)} MB
          </p>
          {video.external_url && (
            <p className="mt-1 truncate font-mono text-xs text-slate-400">{video.external_url}</p>
          )}
        </div>
        <div className="flex shrink-0 items-center gap-2">
          {!isRevoked && !video.is_active && (
            <button
              type="button"
              disabled={pending}
              onClick={onActivate}
              className="rounded-md border border-slate-200 px-3 py-1.5 text-xs font-medium hover:bg-slate-100 disabled:opacity-50"
            >
              Đặt active
            </button>
          )}
          {!isRevoked && (
            <button
              type="button"
              disabled={pending}
              onClick={onRevoke}
              className="rounded-md border border-red-200 px-3 py-1.5 text-xs font-medium text-red-700 hover:bg-red-50 disabled:opacity-50"
            >
              Thu hồi
            </button>
          )}
        </div>
      </div>
    </li>
  );
}
