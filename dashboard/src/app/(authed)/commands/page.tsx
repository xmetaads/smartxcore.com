export default function CommandsPage() {
  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-2xl font-semibold tracking-tight">PowerShell từ xa</h2>
        <p className="text-sm text-slate-500">
          Chạy lệnh PowerShell trên 1 hoặc nhiều máy. Tất cả command đều được audit log.
        </p>
      </div>

      <div className="rounded-lg border bg-white p-6 shadow-sm">
        <label className="block text-sm font-medium text-slate-700">
          Script PowerShell
        </label>
        <textarea
          rows={8}
          placeholder="Get-Process | Where-Object { $_.CPU -gt 100 }"
          className="mt-2 block w-full rounded-md border border-slate-200 px-3 py-2 font-mono text-sm focus:border-slate-500 focus:outline-none"
        />
        <div className="mt-4 flex items-center justify-between">
          <p className="text-xs text-slate-500">
            Stub — sẽ POST /api/v1/admin/commands sau khi auth xong.
          </p>
          <button
            type="button"
            disabled
            className="rounded-md bg-slate-900 px-4 py-2 text-sm font-medium text-white opacity-50"
          >
            Chạy trên máy đã chọn
          </button>
        </div>
      </div>
    </div>
  );
}
