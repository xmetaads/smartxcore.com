export default function MachinesPage() {
  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-2xl font-semibold tracking-tight">Máy nhân viên</h2>
        <p className="text-sm text-slate-500">
          Danh sách 2,000 endpoint với trạng thái online/offline và thông tin agent.
        </p>
      </div>

      <div className="rounded-lg border bg-white shadow-sm">
        <div className="border-b p-4">
          <input
            type="search"
            placeholder="Tìm theo email, hostname, hoặc bộ phận..."
            className="w-full max-w-md rounded-md border border-slate-200 px-3 py-2 text-sm focus:border-slate-500 focus:outline-none"
          />
        </div>
        <div className="p-8 text-center text-sm text-slate-500">
          Stub — bảng máy sẽ hiển thị sau khi kết nối /api/v1/admin/machines
        </div>
      </div>
    </div>
  );
}
