type StatCard = {
  label: string;
  value: string;
  delta?: string;
};

const stats: StatCard[] = [
  { label: "Tổng số máy", value: "—", delta: "đang tải" },
  { label: "Đang online", value: "—" },
  { label: "Offline > 24h", value: "—" },
  { label: "Lỗi gần đây", value: "—" },
];

export default function DashboardPage() {
  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-2xl font-semibold tracking-tight">Tổng quan</h2>
        <p className="text-sm text-slate-500">
          Tình trạng fleet endpoint trong thời gian thực.
        </p>
      </div>

      <div className="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-4">
        {stats.map((stat) => (
          <div key={stat.label} className="rounded-lg border bg-white p-5 shadow-sm">
            <p className="text-sm font-medium text-slate-500">{stat.label}</p>
            <p className="mt-2 text-3xl font-semibold text-slate-900">{stat.value}</p>
            {stat.delta && (
              <p className="mt-1 text-xs text-slate-400">{stat.delta}</p>
            )}
          </div>
        ))}
      </div>

      <div className="rounded-lg border bg-white p-6 shadow-sm">
        <h3 className="text-base font-medium">Hoạt động gần đây</h3>
        <p className="mt-1 text-sm text-slate-500">
          Stub — sẽ kết nối đến /api/v1/admin/activity sau khi backend admin endpoints hoàn tất.
        </p>
      </div>
    </div>
  );
}
