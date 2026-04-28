const API_BASE_URL = process.env.NEXT_PUBLIC_API_BASE_URL ?? "";

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
  token?: string;
};

export async function apiFetch<T>(path: string, opts: FetchOptions = {}): Promise<T> {
  const { body, token, headers, ...rest } = opts;

  const finalHeaders = new Headers(headers);
  finalHeaders.set("Accept", "application/json");
  if (body !== undefined) {
    finalHeaders.set("Content-Type", "application/json");
  }
  if (token) {
    finalHeaders.set("Authorization", `Bearer ${token}`);
  }

  const res = await fetch(`${API_BASE_URL}${path}`, {
    ...rest,
    headers: finalHeaders,
    body: body !== undefined ? JSON.stringify(body) : undefined,
    credentials: "include",
  });

  if (!res.ok) {
    let message = res.statusText;
    try {
      const data = (await res.json()) as { error?: string };
      if (data.error) {
        message = data.error;
      }
    } catch {
      // body was not JSON
    }
    throw new APIError(res.status, message);
  }

  if (res.status === 204) {
    return undefined as T;
  }
  return (await res.json()) as T;
}
