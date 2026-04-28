"use client";

import { useEffect } from "react";

import { bootstrapSession } from "@/lib/api-client";
import { useAuthStore } from "@/lib/auth-store";

// Runs once on mount: tries to recover an authenticated session from the
// HTTP-only refresh cookie. This is what makes "stay logged in across
// reloads" work without putting tokens in localStorage.
export function AuthBootstrap() {
  const setAuth = useAuthStore((s) => s.setAuth);
  const setHydrated = useAuthStore((s) => s.setHydrated);

  useEffect(() => {
    let cancelled = false;
    bootstrapSession()
      .then((res) => {
        if (cancelled) return;
        if (res) {
          setAuth(res.accessToken, {
            id: res.user.id,
            email: res.user.email,
            name: res.user.name,
            role: res.user.role as "admin" | "viewer",
          });
        }
      })
      .finally(() => {
        if (!cancelled) setHydrated();
      });
    return () => {
      cancelled = true;
    };
  }, [setAuth, setHydrated]);

  return null;
}
