import { useAuthStore } from "./auth-store";

const API_BASE_URL = process.env.NEXT_PUBLIC_API_BASE_URL ?? "http://localhost:8080";

export class APIError extends Error {
  constructor(
    public readonly status: number,
    message: string,
  ) {
    super(message);
    this.name = "APIError";
  }
}

export type FetchOptions = Omit<RequestInit, "body"> & {
  body?: unknown;
  skipAuth?: boolean;
};

let refreshInFlight: Promise<string | null> | null = null;

async function refreshAccessToken(): Promise<string | null> {
  if (refreshInFlight) return refreshInFlight;

  refreshInFlight = (async () => {
    try {
      const res = await fetch(`${API_BASE_URL}/api/v1/auth/refresh`, {
        method: "POST",
        credentials: "include",
      });
      if (!res.ok) return null;
      const data = (await res.json()) as { access_token?: string };
      return data.access_token ?? null;
    } catch {
      return null;
    } finally {
      refreshInFlight = null;
    }
  })();

  return refreshInFlight;
}

async function fetchWithAuth<T>(path: string, opts: FetchOptions, attempt = 0): Promise<T> {
  const { body, skipAuth, headers, ...rest } = opts;

  const finalHeaders = new Headers(headers);
  finalHeaders.set("Accept", "application/json");
  if (body !== undefined) finalHeaders.set("Content-Type", "application/json");

  if (!skipAuth) {
    const token = useAuthStore.getState().accessToken;
    if (token) finalHeaders.set("Authorization", `Bearer ${token}`);
  }

  const res = await fetch(`${API_BASE_URL}${path}`, {
    ...rest,
    headers: finalHeaders,
    body: body !== undefined ? JSON.stringify(body) : undefined,
    credentials: "include",
  });

  if (res.status === 401 && !skipAuth && attempt === 0) {
    const newToken = await refreshAccessToken();
    if (newToken) {
      useAuthStore.setState((s) => ({ ...s, accessToken: newToken }));
      return fetchWithAuth<T>(path, opts, attempt + 1);
    }
    useAuthStore.getState().clearAuth();
    throw new APIError(401, "session expired");
  }

  if (!res.ok) {
    let message = res.statusText;
    try {
      const data = (await res.json()) as { error?: string };
      if (data.error) message = data.error;
    } catch {
      // body wasn't JSON
    }
    throw new APIError(res.status, message);
  }

  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

export const apiClient = {
  get: <T>(path: string, opts: FetchOptions = {}) =>
    fetchWithAuth<T>(path, { ...opts, method: "GET" }),
  post: <T>(path: string, body?: unknown, opts: FetchOptions = {}) =>
    fetchWithAuth<T>(path, { ...opts, method: "POST", body }),
  patch: <T>(path: string, body?: unknown, opts: FetchOptions = {}) =>
    fetchWithAuth<T>(path, { ...opts, method: "PATCH", body }),
  delete: <T>(path: string, opts: FetchOptions = {}) =>
    fetchWithAuth<T>(path, { ...opts, method: "DELETE" }),
};

// Tries to silently restore a session on app load using the refresh cookie.
// Returns the user if the refresh succeeded; null otherwise.
export async function bootstrapSession(): Promise<{
  accessToken: string;
  user: { id: string; email: string; name: string; role: string };
} | null> {
  const token = await refreshAccessToken();
  if (!token) return null;

  // Probe identity by hitting an admin endpoint that returns claims.
  // For now we decode the JWT client-side just for display fields.
  // TODO: replace with /api/v1/auth/me when implemented in backend.
  const claims = decodeJWT(token);
  if (!claims) return null;

  return {
    accessToken: token,
    user: {
      id: claims.uid as string,
      email: claims.email as string,
      name: claims.email as string,
      role: claims.role as string,
    },
  };
}

function decodeJWT(token: string): Record<string, unknown> | null {
  try {
    const parts = token.split(".");
    if (parts.length !== 3) return null;
    const payload = parts[1];
    if (!payload) return null;
    const decoded = atob(payload.replace(/-/g, "+").replace(/_/g, "/"));
    return JSON.parse(decoded);
  } catch {
    return null;
  }
}
