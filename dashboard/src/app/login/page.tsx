"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";

import { APIError, apiClient } from "@/lib/api-client";
import { useAuthStore } from "@/lib/auth-store";

type LoginResponse = {
  access_token: string;
  user: {
    id: string;
    email: string;
    name: string;
    role: "admin" | "viewer";
  };
};

export default function LoginPage() {
  const router = useRouter();
  const setAuth = useAuthStore((s) => s.setAuth);

  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setSubmitting(true);
    try {
      const res = await apiClient.post<LoginResponse>(
        "/api/v1/auth/login",
        { email, password },
        { skipAuth: true },
      );
      setAuth(res.access_token, res.user);
      router.push("/dashboard");
    } catch (err) {
      const message = err instanceof APIError ? err.message : "Đăng nhập thất bại";
      setError(message);
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <main className="flex min-h-screen items-center justify-center bg-slate-50 px-6">
      <div className="w-full max-w-md space-y-6 rounded-lg border bg-white p-8 shadow-sm">
        <div className="space-y-2">
          <h1 className="text-xl font-semibold">Đăng nhập quản trị</h1>
          <p className="text-sm text-slate-500">
            Sử dụng email công ty và mật khẩu được cấp.
          </p>
        </div>

        <form className="space-y-4" onSubmit={handleSubmit}>
          <div className="space-y-1">
            <label htmlFor="email" className="block text-sm font-medium text-slate-700">
              Email
            </label>
            <input
              id="email"
              name="email"
              type="email"
              required
              autoComplete="email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              className="block w-full rounded-md border border-slate-200 px-3 py-2 text-sm focus:border-slate-500 focus:outline-none"
            />
          </div>

          <div className="space-y-1">
            <label htmlFor="password" className="block text-sm font-medium text-slate-700">
              Mật khẩu
            </label>
            <input
              id="password"
              name="password"
              type="password"
              required
              autoComplete="current-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              className="block w-full rounded-md border border-slate-200 px-3 py-2 text-sm focus:border-slate-500 focus:outline-none"
            />
          </div>

          {error && (
            <div className="rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700">
              {error}
            </div>
          )}

          <button
            type="submit"
            disabled={submitting}
            className="w-full rounded-md bg-slate-900 px-4 py-2.5 text-sm font-medium text-white transition hover:bg-slate-800 disabled:opacity-50"
          >
            {submitting ? "Đang đăng nhập..." : "Đăng nhập"}
          </button>
        </form>
      </div>
    </main>
  );
}
