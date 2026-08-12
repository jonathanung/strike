import { request } from "./api";

export type PluginMCP = {
  name: string;
  transport?: string;
  command?: string;
  args?: string[];
  envKeys?: string[];
  url?: string;
  headerKeys?: string[];
};

export type PluginHarness = {
  name: string;
  command?: string;
  args?: string[];
};

export type PluginInfo = {
  id: string;
  version?: string;
  name?: string;
  scope?: string;
  enabled: boolean;
  status?: string;
  digest?: string;
  sourceType?: string;
  sourceLabel?: string;
  trustState?: string;
  loadError?: string;
  agents?: number;
  skills?: number;
  workflows?: number;
  themes?: number;
  providers?: number;
  hooks?: number;
  panes?: number;
  mcp?: PluginMCP[];
  harnesses?: PluginHarness[];
  capabilities?: string[];
  hasExecutable?: boolean;
  findings?: string[];
  updateAvailable?: string;
};

export type PluginTrustPreview = {
  id: string;
  scope?: string;
  digest?: string;
  capabilities?: string[];
  mcp?: PluginMCP[];
  harnesses?: PluginHarness[];
  hooks?: number;
  reviewLines?: string[];
};

export type PluginUpdateReview = {
  id: string;
  oldVersion?: string;
  newVersion?: string;
  summary?: string;
  trustInvalidated?: boolean;
  capabilityAdded?: string[];
  capabilityRemoved?: string[];
};

export type PluginInstallResult = {
  id: string;
  version?: string;
  scope?: string;
  digest?: string;
  enabled: boolean;
};

export type PluginCatalogHit = {
  id: string;
  name?: string;
  description?: string;
  version?: string;
  registry?: string;
  capabilities?: string[];
};

export const listPlugins = () => request<{ plugins: PluginInfo[] }>("/v1/plugins");

export const inspectPlugin = (id: string, scope?: string) => {
  const q = scope ? `?scope=${encodeURIComponent(scope)}` : "";
  return request<PluginInfo>(`/v1/plugins/${encodeURIComponent(id)}${q}`);
};

export const enablePlugin = (id: string, scope?: string) =>
  request<{ ok: boolean }>("/v1/plugins/enable", {
    method: "POST",
    body: JSON.stringify({ id, scope: scope || undefined }),
  });

export const disablePlugin = (id: string, scope?: string) =>
  request<{ ok: boolean }>("/v1/plugins/disable", {
    method: "POST",
    body: JSON.stringify({ id, scope: scope || undefined }),
  });

export const removePlugin = (id: string, scope: string | undefined, confirm: boolean) =>
  request<{ ok: boolean }>("/v1/plugins/remove", {
    method: "POST",
    body: JSON.stringify({ id, scope: scope || undefined, confirm }),
  });

export const trustPreviewPlugin = (id: string, scope?: string) => {
  const q = scope ? `?scope=${encodeURIComponent(scope)}` : "";
  return request<PluginTrustPreview>(`/v1/plugins/${encodeURIComponent(id)}/trust-preview${q}`);
};

export const trustPlugin = (id: string, scope?: string) =>
  request<{ ok: boolean }>("/v1/plugins/trust", {
    method: "POST",
    body: JSON.stringify({ id, scope: scope || undefined }),
  });

export const untrustPlugin = (id: string, scope?: string) =>
  request<{ ok: boolean }>("/v1/plugins/untrust", {
    method: "POST",
    body: JSON.stringify({ id, scope: scope || undefined }),
  });

export const searchPlugins = (registry: string, query: string) =>
  request<{ hits: PluginCatalogHit[] }>("/v1/plugins/search", {
    method: "POST",
    body: JSON.stringify({ registry, query }),
  });

export const installPlugin = (source: string, scope?: string, registry?: string) =>
  request<PluginInstallResult>("/v1/plugins/install", {
    method: "POST",
    body: JSON.stringify({ source, scope: scope || undefined, registry: registry || undefined }),
  });

export const previewUpdatePlugin = (id: string, scope?: string, registry?: string) =>
  request<PluginUpdateReview>("/v1/plugins/preview-update", {
    method: "POST",
    body: JSON.stringify({ id, scope: scope || undefined, registry: registry || undefined }),
  });

export const updatePlugin = (id: string, scope: string | undefined, registry: string | undefined, confirm: boolean) =>
  request<PluginInstallResult>("/v1/plugins/update", {
    method: "POST",
    body: JSON.stringify({ id, scope: scope || undefined, registry: registry || undefined, confirm }),
  });
