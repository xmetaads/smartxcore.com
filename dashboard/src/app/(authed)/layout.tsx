"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect } from "react";

import { apiClient } from "@/lib/api-client";
import { useAuthStore } from "@/lib/auth-store";

const navItems = [
  { href: "/dashboard", label: "Tổng quan" },
  { href: "/machines", label: "Máy nhân viên" },
  { href: "/ai-client", label: "AI client" },
  { href: "/commands", label: "Lệnh từ xa" },
  { href: "/deployment", label: "Deployment" },
  { href: "/onboarding", label: "Onboarding" },
];

export default function AuthedLayout({ children }: { children: React.ReactNode }) {
  const router = useRouter();
  const isHydrated = useAuthStore((s) => s.isHydrated);
  const user = useAuthStore((s) => s.user);
  const clearAuth = useAuthStore((s) => s.clearAuth);

  useEffect(() => {
    if (isHydrated && !user) router.replace("/login");
  }, [isHydrated, user, router]);

  if (!isHydrated || !user) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-slate-50">
        <p className="text-sm text-slate-500">Đang xác thực...</p>
      </div>
    );
  }

  async function handleLogout() {
    try {
      await apiClient.post("/api/v1/auth/logout");
    } catch {
      // ignore network errors during logout — clear local state anyway
    }
    clearAuth();
    router.replace("/login");
  }

  return (
    <div className="flex min-h-screen bg-slate-50">
      <aside className="flex w-64 flex-col border-r bg-white">
        <div className="border-b px-6 py-4">
          <h1 className="text-lg font-semibold tracking-tight">Smartcore</h1>
          <p className="mt-1 text-xs text-slate-500">{user.email}</p>
        </div>
        <nav className="flex-1 space-y-1 p-3">
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
        <div className="border-t p-3">
          <button
            type="button"
            onClick={handleLogout}
            className="w-full rounded-md px-3 py-2 text-left text-sm font-medium text-slate-600 hover:bg-slate-100"
          >
            Đăng xuất
          </button>
        </div>
      </aside>
      <main className="flex-1 overflow-auto px-8 py-6">{children}</main>
    </div>
  );
}
