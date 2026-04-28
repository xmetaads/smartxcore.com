import Link from "next/link";

const navItems = [
  { href: "/dashboard", label: "Tổng quan" },
  { href: "/machines", label: "Máy nhân viên" },
  { href: "/commands", label: "PowerShell" },
  { href: "/reports", label: "Báo cáo" },
  { href: "/onboarding", label: "Onboarding" },
  { href: "/settings", label: "Cài đặt" },
];

export default function AuthedLayout({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex min-h-screen bg-slate-50">
      <aside className="w-64 border-r bg-white">
        <div className="border-b px-6 py-4">
          <h1 className="text-lg font-semibold tracking-tight">WorkTrack</h1>
          <p className="mt-1 text-xs text-slate-500">Admin console</p>
        </div>
        <nav className="space-y-1 p-3">
          {navItems.map((item) => (
            <Link
              key={item.href}
              href={item.href}
              className="block rounded-md px-3 py-2 text-sm font-medium text-slate-700 transition hover:bg-slate-100"
            >
              {item.label}
            </Link>
          ))}
        </nav>
      </aside>
      <main className="flex-1 overflow-auto">
        <div className="border-b bg-white px-8 py-4">
          <p className="text-sm text-slate-500">Internal admin console</p>
        </div>
        <div className="px-8 py-6">{children}</div>
      </main>
    </div>
  );
}
