/** Provider auth / custom providers / model metadata client (WEBUI.10). */
import { request } from "./api";

export type ProviderStatus = {
  name: string;
  detail: string;
  authed: boolean;
  builtin?: boolean;
  custom?: boolean;
  oauth?: boolean;
  device?: boolean;
  apiKey?: boolean;
  wireAPI?: string;
  baseURL?: string;
  expiresAt?: string;
  methods?: string[];
};

export type ProvidersResponse = {
  providers?: ProviderStatus[];
  oauthMode?: string;
  oauthNote?: string;
};

export type ModelInfo = {
  id?: string;
  ID?: string;
  provider?: string;
  Provider?: string;
  name?: string;
  Name?: string;
  context?: number;
  Context?: number;
  output?: number;
  Output?: number;
  inputCost?: number;
  InputCost?: number;
  outputCost?: number;
  OutputCost?: number;
  hasCost?: boolean;
  HasCost?: boolean;
  toolCall?: boolean;
  ToolCall?: boolean;
  reasoning?: boolean;
  Reasoning?: boolean;
  attachment?: boolean;
  Attachment?: boolean;
  variantIds?: string[];
  VariantIDs?: string[];
  source?: string;
  Source?: string;
};

export type DeviceLogin = {
  id: string;
  provider: string;
  userCode?: string;
  verificationUri?: string;
  expiresAt?: string;
  status: string;
  message?: string;
  providerStatus?: ProviderStatus;
};

export type OAuthLogin = {
  id: string;
  provider: string;
  authorizeUrl?: string;
  expiresAt?: string;
  status: string;
  message?: string;
  note?: string;
  mode?: string;
  providerStatus?: ProviderStatus;
};

export type CustomProvider = {
  name: string;
  baseURL: string;
  api: string;
  headers?: Record<string, string>;
  apiKeyEnv?: string;
  models?: string[];
};

export type PermissionPreset = {
  id?: string;
  ID?: string;
  name?: string;
  Name?: string;
  description?: string;
  Description?: string;
};

export type SchedulerPreset = {
  id?: string;
  ID?: string;
  name?: string;
  Name?: string;
  rationale?: string;
  Rationale?: string;
};

export type ConfigSource = {
  slot?: string;
  kind?: string;
  scope?: string;
  label?: string;
  display?: string;
  exists?: boolean;
  canCreate?: boolean;
};

export const fetchProviders = () => request<ProvidersResponse>("/v1/providers");

export const setAPIKey = (provider: string, key: string) =>
  request<{ ok: boolean; provider?: ProviderStatus }>("/v1/auth/key", {
    method: "POST",
    body: JSON.stringify({ provider, key }),
  });

export const logoutProvider = (provider: string) =>
  request<{ ok: boolean; provider?: ProviderStatus }>(`/v1/auth/${encodeURIComponent(provider)}`, {
    method: "DELETE",
  });

export const startDevice = (provider: string) =>
  request<DeviceLogin>("/v1/auth/device", {
    method: "POST",
    body: JSON.stringify({ provider }),
  });

export const deviceStatus = (id: string) =>
  request<DeviceLogin>(`/v1/auth/device/${encodeURIComponent(id)}`);

export const cancelDevice = (id: string) =>
  request<{ ok: boolean }>(`/v1/auth/device/${encodeURIComponent(id)}`, { method: "DELETE" });

export const startOAuth = (provider: string) =>
  request<OAuthLogin>("/v1/auth/oauth", {
    method: "POST",
    body: JSON.stringify({ provider }),
  });

export const oauthStatus = (id: string) =>
  request<OAuthLogin>(`/v1/auth/oauth/${encodeURIComponent(id)}`);

export const completeOAuth = (id: string, paste: string) =>
  request<{ ok: boolean; providerStatus?: ProviderStatus }>(
    `/v1/auth/oauth/${encodeURIComponent(id)}/complete`,
    { method: "POST", body: JSON.stringify({ paste }) },
  );

export const cancelOAuth = (id: string) =>
  request<{ ok: boolean }>(`/v1/auth/oauth/${encodeURIComponent(id)}`, { method: "DELETE" });

export const fetchModels = (provider?: string) => {
  const qs = provider ? `?provider=${encodeURIComponent(provider)}` : "";
  return request<{ models: ModelInfo[] }>(`/v1/models${qs}`);
};

export const listCustomProviders = () =>
  request<{ providers: CustomProvider[] }>("/v1/custom-providers");

export const upsertCustomProvider = (p: CustomProvider) =>
  request<CustomProvider>("/v1/custom-providers", {
    method: "POST",
    body: JSON.stringify(p),
  });

export const removeCustomProvider = (name: string) =>
  request<void>(`/v1/custom-providers/${encodeURIComponent(name)}`, { method: "DELETE" });

export const fetchPermissionPresets = () =>
  request<{ presets: PermissionPreset[] }>("/v1/permissions/presets");

export const explainPermission = (permission: string, pattern = "*", preset = "") => {
  const qs = new URLSearchParams({ permission, pattern });
  if (preset) qs.set("preset", preset);
  return request<Record<string, unknown>>(`/v1/permissions/explain?${qs}`);
};

export const fetchSchedulerPresets = () =>
  request<{ presets: SchedulerPreset[]; global?: { presets?: string[] } }>("/v1/scheduler/presets");

export const applySchedulerPresets = (ids: string[]) =>
  request<{ ok: boolean }>("/v1/scheduler/presets", {
    method: "POST",
    body: JSON.stringify({ ids }),
  });

export const fetchConfigSources = (rootID?: string) => {
  const qs = rootID ? `?root=${encodeURIComponent(rootID)}` : "";
  return request<{ sources: ConfigSource[] }>(`/v1/config-sources${qs}`);
};

export function modelId(m: ModelInfo): string {
  return String(m.id || m.ID || "").trim();
}

export function modelMetaLine(m: ModelInfo): string {
  const parts: string[] = [];
  const ctx = m.context ?? m.Context;
  const out = m.output ?? m.Output;
  if (ctx) parts.push(`ctx ${ctx.toLocaleString()}`);
  if (out) parts.push(`out ${out.toLocaleString()}`);
  if (m.toolCall || m.ToolCall) parts.push("tools");
  if (m.reasoning || m.Reasoning) parts.push("reasoning");
  if (m.attachment || m.Attachment) parts.push("attachments");
  const hasCost = m.hasCost ?? m.HasCost;
  if (hasCost) {
    const ic = m.inputCost ?? m.InputCost ?? 0;
    const oc = m.outputCost ?? m.OutputCost ?? 0;
    parts.push(`$${ic}/$${oc} per M`);
  }
  const src = m.source || m.Source;
  if (src) parts.push(src);
  return parts.length ? parts.join(" · ") : "limits unknown";
}
