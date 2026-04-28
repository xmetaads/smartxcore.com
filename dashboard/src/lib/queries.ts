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
};

export function listDeploymentTokens(includeRevoked = false) {
  const qs = includeRevoked ? "?include_revoked=true" : "";
  return apiClient.get<{ items: DeploymentToken[] }>(`/api/v1/admin/deployment-tokens${qs}`);
}

export function createDeploymentToken(input: {
  name: string;
  description?: string;
  ttl_days: number;
  max_uses?: number;
  allowed_email_domains?: string[];
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

// === Commands ===

export type CommandStatus =
  | "pending"
  | "dispatched"
  | "running"
  | "completed"
  | "failed"
  | "timeout"
  | "cancelled";

export type Command = {
  id: string;
  machine_id: string;
  status: CommandStatus;
  script_content: string;
  exit_code?: number | null;
  stdout?: string | null;
  stderr?: string | null;
  started_at?: string | null;
  completed_at?: string | null;
};

export function createCommand(input: {
  machine_ids: string[];
  script_content: string;
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
