"use client";

import { create } from "zustand";

export type AuthUser = {
  id: string;
  email: string;
  name: string;
  role: "admin" | "viewer";
};

type AuthState = {
  accessToken: string | null;
  user: AuthUser | null;
  isHydrated: boolean;
  setAuth: (token: string, user: AuthUser) => void;
  clearAuth: () => void;
  setHydrated: () => void;
};

// Access token lives in memory only — refresh token sits in an HTTP-only
// cookie set by the backend, which is what we use to recover state on
// reload. This avoids the XSS exposure of localStorage tokens.
export const useAuthStore = create<AuthState>((set) => ({
  accessToken: null,
  user: null,
  isHydrated: false,
  setAuth: (token, user) => set({ accessToken: token, user }),
  clearAuth: () => set({ accessToken: null, user: null }),
  setHydrated: () => set({ isHydrated: true }),
}));
