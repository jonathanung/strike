import { FormEvent, useEffect, useState } from "react";
import { downloadDiagnostics, request } from "./api";
import type { Bootstrap, Status } from "./types";
import { Button, CapabilityUnavailable, Dialog, LoadingState } from "./ui";
import {
  applySchedulerPresets,
  cancelDevice,
  cancelOAuth,
  completeOAuth,
  deviceStatus,
  explainPermission,
  fetchConfigSources,
  fetchModels,
  fetchPermissionPresets,
  fetchProviders,
  fetchSchedulerPresets,
  listCustomProviders,
  logoutProvider,
  modelId,
  modelMetaLine,
  oauthStatus,
  removeCustomProvider,
  setAPIKey,
  startDevice,
  startOAuth,
  upsertCustomProvider,
  type ConfigSource,
  type CustomProvider,
  type DeviceLogin,
  type ModelInfo,
  type OAuthLogin,
  type PermissionPreset,
  type ProviderStatus,
  type SchedulerPreset,
} from "./providers";

export type UserSettings = {
  provider?: string;
  model?: string;
  agent?: string;
  effort?: string;
  mode?: string;
  sandbox?: string;
  notify?: string;
  autoupdate?: string;
  leanCode?: string;
  deferTools?: string;
  sessionWorktree?: string;
  permissionAutoApproveSeconds?: number;
  permissionAutoApproveExclude?: string[];
  maxChildDepth?: number;
  theme?: string;
  compactionStrategy?: string;
  compactionModel?: string;
  compactionThreshold?: number;
  compactionBuffer?: number;
  keepUserTurns?: number;
  pruneProtectTokens?: number;
  pruneMinimumTokens?: number;
  pruneKeepUserTurns?: number;
  pruneProtectTools?: string[];
};

const effortValues = ["", "off", "low", "medium", "high", "xhigh"];
const modeValues = ["", "default", "plan", "soft-approve", "accept-edits", "yolo"];
const sandboxValues = ["", "workspace-write", "read-only", "off"];
const notifyValues = ["", "unfocused-only", "on", "off"];
const leanCodeValues = ["", "lite", "full", "off"];
const autoApproveValues = ["", "off", "5", "10", "15", "30", "45", "60"];
const maxChildDepthValues = ["", "default", "1", "2", "3", "4", "5", "6", "7", "8"];
const compactionStrategyValues = ["", "trim", "summarize"];
const thresholdValues = ["", "default", "0.60", "0.70", "0.80", "0.85", "1"];
const bufferValues = ["", "default", "1024", "2048", "4096", "8192"];
const keepTurnsValues = ["", "default", "1", "2", "3", "4"];
const excludeChoices = ["bash", "write", "edit", "webfetch", "task", "skill"];
const appearanceValues = ["auto", "dark", "light"] as const;
export type Appearance = (typeof appearanceValues)[number];

const APPEARANCE_KEY = "strike.web.appearance";

export function loadAppearance(): Appearance {
  try {
    const v = localStorage.getItem(APPEARANCE_KEY);
    if (v === "dark" || v === "light" || v === "auto") return v;
  } catch { /* ignore */ }
  return "auto";
}

export function applyAppearance(value: Appearance) {
  const root = document.documentElement;
  if (value === "auto") root.removeAttribute("data-appearance");
  else root.setAttribute("data-appearance", value);
  try { localStorage.setItem(APPEARANCE_KEY, value); } catch { /* ignore */ }
}

function SelectField({
  label, value, values, disabled, onChange,
}: {
  label: string; value: string; values: string[]; disabled?: boolean; onChange: (value: string) => void;
}) {
  return (
    <label>
      {label}
      <select aria-label={label} value={value} disabled={disabled} onChange={(e) => onChange(e.target.value)}>
        {values.map((item) => (
          <option key={item || "(unset)"} value={item}>{item || "— leave unchanged"}</option>
        ))}
      </select>
    </label>
  );
}

function TextField({
  label, value, disabled, placeholder, onChange,
}: {
  label: string; value: string; disabled?: boolean; placeholder?: string; onChange: (value: string) => void;
}) {
  return (
    <label>
      {label}
      <input aria-label={label} value={value} disabled={disabled} placeholder={placeholder} onChange={(e) => onChange(e.target.value)} />
    </label>
  );
}

function dialOrEmpty(n: number | undefined, zeroAs = "off"): string {
  if (n === undefined || n === null) return "";
  if (n === 0) return zeroAs;
  return String(n);
}

function floatDial(n: number | undefined): string {
  if (n === undefined || n === null || n === 0) return "";
  return String(n);
}

export function SettingsDialog({
  boot, status, providers, onClose, rootID, isLive,
}: {
  boot?: Bootstrap; status: Status; providers: string[]; onClose: () => void; rootID?: string; isLive?: boolean;
}) {
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [appearance, setAppearance] = useState<Appearance>(() => loadAppearance());

  // Provider auth (WEBUI.10)
  const [providerRows, setProviderRows] = useState<ProviderStatus[]>([]);
  const [oauthNote, setOauthNote] = useState("");
  const [authProvider, setAuthProvider] = useState(String(status.provider || providers[0] || ""));
  const [authKey, setAuthKey] = useState("");
  const [authBusy, setAuthBusy] = useState(false);
  const [deviceFlow, setDeviceFlow] = useState<DeviceLogin | null>(null);
  const [oauthFlow, setOauthFlow] = useState<OAuthLogin | null>(null);
  const [oauthPaste, setOauthPaste] = useState("");
  const [modelInfos, setModelInfos] = useState<ModelInfo[]>([]);
  const [customProviders, setCustomProviders] = useState<CustomProvider[]>([]);
  const [customForm, setCustomForm] = useState({ name: "", baseURL: "", api: "openai", models: "" });
  const [permPresets, setPermPresets] = useState<PermissionPreset[]>([]);
  const [permExplain, setPermExplain] = useState("");
  const [permTool, setPermTool] = useState("bash");
  const [permPattern, setPermPattern] = useState("*");
  const [permPreset, setPermPreset] = useState("");
  const [schedPresets, setSchedPresets] = useState<SchedulerPreset[]>([]);
  const [schedEnabled, setSchedEnabled] = useState<string[]>([]);
  const [configSources, setConfigSources] = useState<ConfigSource[]>([]);

  // Runtime defaults
  const [defProvider, setDefProvider] = useState("");
  const [defModel, setDefModel] = useState("");
  const [defAgent, setDefAgent] = useState("");
  const [defEffort, setDefEffort] = useState("");
  const [defMode, setDefMode] = useState("");

  // Config dials
  const [sandbox, setSandbox] = useState("");
  const [notify, setNotify] = useState("");
  const [leanCode, setLeanCode] = useState("");

  // Auto-approve
  const [autoSecs, setAutoSecs] = useState("");
  const [exclude, setExclude] = useState<string[]>([]);
  const [excludeDirty, setExcludeDirty] = useState(false);
  const [maxChildDepth, setMaxChildDepth] = useState("");

  // Compaction
  const [compStrategy, setCompStrategy] = useState("");
  const [compModel, setCompModel] = useState("");
  const [compThreshold, setCompThreshold] = useState("");
  const [compBuffer, setCompBuffer] = useState("");
  const [keepUserTurns, setKeepUserTurns] = useState("");
  const [pruneProtectTools, setPruneProtectTools] = useState("");

  const settingsCap = Boolean(boot?.capabilities.settings);

  const authCap = Boolean(boot?.capabilities.auth);
  const providersCap = Boolean(boot?.capabilities.providers);
  const permissionsCap = Boolean(boot?.capabilities.permissions);
  const schedulerCap = Boolean(boot?.capabilities.scheduler);
  const configFilesCap = Boolean(boot?.capabilities.configFiles);
  const mutateOK = Boolean(boot && !boot.attachOnly);

  const refreshProviders = async () => {
    if (!authCap) return;
    try {
      const res = await fetchProviders();
      setProviderRows(res.providers || []);
      setOauthNote(res.oauthNote || "");
      if (!authProvider && res.providers?.length) {
        setAuthProvider(res.providers[0].name);
      }
    } catch (err) {
      setError((err as Error).message);
    }
  };

  useEffect(() => {
    void refreshProviders();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [authCap]);

  useEffect(() => {
    if (!authCap || !authProvider) return;
    void fetchModels(authProvider)
      .then((r) => setModelInfos(r.models || []))
      .catch(() => setModelInfos([]));
  }, [authCap, authProvider]);

  useEffect(() => {
    if (!providersCap) return;
    void listCustomProviders()
      .then((r) => setCustomProviders(r.providers || []))
      .catch(() => setCustomProviders([]));
  }, [providersCap]);

  useEffect(() => {
    if (!permissionsCap) return;
    void fetchPermissionPresets()
      .then((r) => setPermPresets(r.presets || []))
      .catch(() => setPermPresets([]));
  }, [permissionsCap]);

  useEffect(() => {
    if (!schedulerCap) return;
    void fetchSchedulerPresets()
      .then((r) => {
        setSchedPresets(r.presets || []);
        setSchedEnabled([...(r.global?.presets || [])]);
      })
      .catch(() => { /* optional */ });
  }, [schedulerCap]);

  useEffect(() => {
    if (!configFilesCap) return;
    void fetchConfigSources(rootID)
      .then((r) => setConfigSources(r.sources || []))
      .catch(() => setConfigSources([]));
  }, [configFilesCap, rootID]);

  // Poll device/oauth flows
  useEffect(() => {
    if (!deviceFlow || deviceFlow.status !== "pending") return;
    const id = window.setInterval(() => {
      void deviceStatus(deviceFlow.id).then((d) => {
        setDeviceFlow(d);
        if (d.status === "completed") void refreshProviders();
      }).catch(() => {});
    }, 1500);
    return () => window.clearInterval(id);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [deviceFlow?.id, deviceFlow?.status]);

  useEffect(() => {
    if (!oauthFlow || oauthFlow.status !== "pending") return;
    const id = window.setInterval(() => {
      void oauthStatus(oauthFlow.id).then((d) => {
        setOauthFlow(d);
        if (d.status === "completed") void refreshProviders();
      }).catch(() => {});
    }, 1500);
    return () => window.clearInterval(id);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [oauthFlow?.id, oauthFlow?.status]);

  const agents = boot?.agents?.map((a) => a.name) || [];

  useEffect(() => {
    if (!settingsCap) return;
    setLoading(true);
    setError("");
    request<UserSettings>("/v1/settings")
      .then((s) => {
        setDefProvider(s.provider || "");
        setDefModel(s.model || "");
        setDefAgent(s.agent || "");
        setDefEffort(s.effort || "");
        setDefMode(s.mode || "");
        setSandbox(s.sandbox || "");
        setNotify(s.notify || "");
        setLeanCode(s.leanCode || "");
        setAutoSecs(dialOrEmpty(s.permissionAutoApproveSeconds, "off"));
        setExclude([...(s.permissionAutoApproveExclude || [])]);
        setExcludeDirty(false);
        setMaxChildDepth(dialOrEmpty(s.maxChildDepth, "default"));
        setCompStrategy(s.compactionStrategy || "");
        setCompModel(s.compactionModel || "");
        setCompThreshold(floatDial(s.compactionThreshold));
        setCompBuffer(dialOrEmpty(s.compactionBuffer, "default"));
        setKeepUserTurns(dialOrEmpty(s.keepUserTurns, "default"));
        setPruneProtectTools((s.pruneProtectTools || []).join(", "));
      })
      .catch((err: Error) => setError(err.message))
      .finally(() => setLoading(false));
  }, [settingsCap]);

  const onAppearance = (value: Appearance) => {
    setAppearance(value);
    applyAppearance(value);
  };

  const toggleExclude = (name: string) => {
    setExcludeDirty(true);
    setExclude((old) => (old.includes(name) ? old.filter((x) => x !== name) : [...old, name]));
  };

  const save = async (event?: FormEvent) => {
    event?.preventDefault();
    if (!settingsCap) { onClose(); return; }
    setSaving(true);
    setError("");
    const body: Record<string, unknown> = {};
    if (defProvider) body.provider = defProvider;
    if (defModel) body.model = defModel;
    if (defAgent) body.agent = defAgent;
    if (defEffort) body.effort = defEffort;
    if (defMode) body.mode = defMode;
    if (sandbox) body.sandbox = sandbox;
    if (notify) body.notify = notify;
    if (leanCode) body.leanCode = leanCode;
    if (autoSecs) body.permissionAutoApproveSeconds = autoSecs;
    if (excludeDirty) body.permissionAutoApproveExclude = exclude;
    if (maxChildDepth) body.maxChildDepth = maxChildDepth;
    if (compStrategy) body.compactionStrategy = compStrategy;
    if (compModel.trim()) body.compactionModel = compModel.trim();
    if (compThreshold) body.compactionThreshold = compThreshold;
    if (compBuffer) body.compactionBuffer = compBuffer;
    if (keepUserTurns) body.keepUserTurns = keepUserTurns;
    if (pruneProtectTools.trim()) body.pruneProtectTools = pruneProtectTools.trim();
    // Also allow "save current runtime as defaults" when form defaults empty but status live.
    if (!body.provider && !body.model && !body.agent && !body.effort && !body.mode
      && status.provider && Object.keys(body).length === 0) {
      body.provider = String(status.provider || "");
      body.model = String(status.model || "");
      body.agent = String(status.agent || "");
      body.effort = String(status.effort || "");
      body.mode = String(status.permissionMode || "");
    }
    try {
      if (Object.keys(body).length > 0) {
        await request<UserSettings>("/v1/settings", { method: "PATCH", body: JSON.stringify(body) });
      }
      onClose();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const saveRuntimeDefaults = async () => {
    if (!settingsCap) return;
    setSaving(true);
    setError("");
    try {
      await request<UserSettings>("/v1/settings", {
        method: "PATCH",
        body: JSON.stringify({
          provider: String(status.provider || ""),
          model: String(status.model || ""),
          agent: String(status.agent || ""),
          effort: String(status.effort || ""),
          mode: String(status.permissionMode || ""),
        }),
      });
      setDefProvider(String(status.provider || ""));
      setDefModel(String(status.model || ""));
      setDefAgent(String(status.agent || ""));
      setDefEffort(String(status.effort || ""));
      setDefMode(String(status.permissionMode || ""));
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const footerActions = (!settingsCap || loading) ? (
    <Button type="button" onClick={onClose}>Close</Button>
  ) : undefined;

  return (
    <Dialog
      title="Workspace settings"
      className="settings-dialog"
      wide
      mode="modal"
      onClose={onClose}
      actions={footerActions}
    >
      {authCap ? (
        <fieldset>
          <legend>Provider authentication</legend>
          <p className="muted">Credential values are never shown. Methods reflect host capabilities.</p>
          <div className="provider-rows" role="list" aria-label="Providers">
            {providerRows.map((row) => (
              <article key={row.name} className="provider-row" role="listitem">
                <header>
                  <strong>{row.name}</strong>
                  <span className={row.authed ? "badge ok" : "badge"}>{row.authed ? "authenticated" : row.detail || "none"}</span>
                </header>
                <p className="muted">
                  methods: {(row.methods || []).join(", ") || "—"}
                  {row.expiresAt ? ` · expires ${row.expiresAt}` : ""}
                  {row.custom ? " · custom" : ""}
                  {row.baseURL ? ` · ${row.baseURL}` : ""}
                </p>
                <div className="panel-actions">
                  <Button type="button" disabled={!mutateOK || authBusy} onClick={() => setAuthProvider(row.name)}>Select</Button>
                  {row.authed && !row.builtin && (
                    <Button
                      type="button"
                      disabled={!mutateOK || authBusy}
                      onClick={() => {
                        setAuthBusy(true);
                        void logoutProvider(row.name)
                          .then(() => refreshProviders())
                          .catch((err: Error) => setError(err.message))
                          .finally(() => setAuthBusy(false));
                      }}
                    >
                      Log out
                    </Button>
                  )}
                </div>
              </article>
            ))}
            {!providerRows.length && <p className="muted">No providers reported.</p>}
          </div>

          <label>
            Selected provider
            <select
              value={authProvider}
              onChange={(e) => setAuthProvider(e.target.value)}
              aria-label="Auth provider"
              disabled={!mutateOK}
            >
              {providerRows.map((row) => <option key={row.name} value={row.name}>{row.name}</option>)}
              {!providerRows.length && providers.map((name) => <option key={name} value={name}>{name}</option>)}
            </select>
          </label>

          {providerRows.find((r) => r.name === authProvider)?.apiKey !== false && (
            <>
              <label>
                API key
                <input
                  value={authKey}
                  onChange={(e) => setAuthKey(e.target.value)}
                  placeholder="Stored locally by strike — never echoed back"
                  aria-label="API key"
                  disabled={!mutateOK || authBusy}
                  autoComplete="off"
                />
              </label>
              <Button
                type="button"
                disabled={!mutateOK || !authProvider || !authKey || authBusy}
                onClick={() => {
                  setAuthBusy(true);
                  setError("");
                  void setAPIKey(authProvider, authKey)
                    .then(() => { setAuthKey(""); return refreshProviders(); })
                    .catch((err: Error) => setError(err.message))
                    .finally(() => setAuthBusy(false));
                }}
              >
                Save key
              </Button>
            </>
          )}

          {providerRows.find((r) => r.name === authProvider)?.device && (
            <div className="auth-device">
              <Button
                type="button"
                disabled={!mutateOK || authBusy || Boolean(deviceFlow && deviceFlow.status === "pending")}
                onClick={() => {
                  setAuthBusy(true);
                  setError("");
                  void startDevice(authProvider)
                    .then((d) => setDeviceFlow(d))
                    .catch((err: Error) => setError(err.message))
                    .finally(() => setAuthBusy(false));
                }}
              >
                Start device login
              </Button>
              {deviceFlow && (
                <div className="device-flow" role="status" aria-live="polite">
                  <p>Status: <strong>{deviceFlow.status}</strong></p>
                  {deviceFlow.userCode && <p>Code: <code>{deviceFlow.userCode}</code></p>}
                  {deviceFlow.verificationUri && (
                    <p>
                      Open{" "}
                      <a href={deviceFlow.verificationUri} target="_blank" rel="noopener noreferrer">
                        {deviceFlow.verificationUri}
                      </a>
                    </p>
                  )}
                  {deviceFlow.expiresAt && <p className="muted">Expires {deviceFlow.expiresAt}</p>}
                  {deviceFlow.message && <p className="settings-error">{deviceFlow.message}</p>}
                  {deviceFlow.status === "pending" && (
                    <Button
                      type="button"
                      onClick={() => {
                        void cancelDevice(deviceFlow.id).then(() => setDeviceFlow({ ...deviceFlow, status: "canceled" }));
                      }}
                    >
                      Cancel
                    </Button>
                  )}
                </div>
              )}
            </div>
          )}

          {providerRows.find((r) => r.name === authProvider)?.oauth && (
            <div className="auth-oauth">
              <p className="muted">{oauthNote || "OAuth uses authorize URL + optional paste; no invented browser callback."}</p>
              <Button
                type="button"
                disabled={!mutateOK || authBusy}
                onClick={() => {
                  setAuthBusy(true);
                  setError("");
                  void startOAuth(authProvider)
                    .then((d) => setOauthFlow(d))
                    .catch((err: Error) => setError(err.message))
                    .finally(() => setAuthBusy(false));
                }}
              >
                Start OAuth
              </Button>
              {oauthFlow && (
                <div className="oauth-flow" role="status">
                  <p>Status: <strong>{oauthFlow.status}</strong></p>
                  {oauthFlow.authorizeUrl && (
                    <p>
                      <a href={oauthFlow.authorizeUrl} target="_blank" rel="noopener noreferrer">Open authorize URL</a>
                    </p>
                  )}
                  {oauthFlow.status === "pending" && (
                    <>
                      <label>
                        Paste redirect URL or code
                        <input
                          value={oauthPaste}
                          onChange={(e) => setOauthPaste(e.target.value)}
                          aria-label="OAuth paste"
                          autoComplete="off"
                        />
                      </label>
                      <Button
                        type="button"
                        disabled={!oauthPaste.trim()}
                        onClick={() => {
                          void completeOAuth(oauthFlow.id, oauthPaste)
                            .then(() => { setOauthPaste(""); return oauthStatus(oauthFlow.id); })
                            .then((d) => { setOauthFlow(d); return refreshProviders(); })
                            .catch((err: Error) => setError(err.message));
                        }}
                      >
                        Complete with paste
                      </Button>
                      <Button type="button" onClick={() => void cancelOAuth(oauthFlow.id).then(() => setOauthFlow({ ...oauthFlow, status: "canceled" }))}>
                        Cancel
                      </Button>
                    </>
                  )}
                  {oauthFlow.message && <p className="muted">{oauthFlow.message}</p>}
                </div>
              )}
            </div>
          )}

          {!mutateOK && <p className="muted">Attach-only is read-only — status is visible; credential mutations are disabled.</p>}

          {modelInfos.length > 0 && (
            <div className="model-meta" aria-label="Model metadata">
              <h3>Models ({authProvider})</h3>
              <ul>
                {modelInfos.slice(0, 24).map((m) => (
                  <li key={modelId(m)}>
                    <code>{modelId(m)}</code>
                    <span className="muted"> {modelMetaLine(m)}</span>
                  </li>
                ))}
              </ul>
            </div>
          )}
        </fieldset>
      ) : (
        <CapabilityUnavailable name="Provider authentication" />
      )}

      {providersCap && (
        <fieldset>
          <legend>Custom providers</legend>
          <p className="muted">Config only — set API keys via the auth section using the provider name. Credentials are never listed.</p>
          <ul className="custom-provider-list">
            {customProviders.map((cp) => (
              <li key={cp.name}>
                <code>{cp.name}</code> · {cp.api} · {cp.baseURL}
                <Button
                  type="button"
                  disabled={!mutateOK}
                  onClick={() => {
                    void removeCustomProvider(cp.name)
                      .then(() => listCustomProviders())
                      .then((r) => setCustomProviders(r.providers || []))
                      .then(() => refreshProviders())
                      .catch((err: Error) => setError(err.message));
                  }}
                >
                  Remove
                </Button>
              </li>
            ))}
          </ul>
          <label>Name<input aria-label="Custom provider name" value={customForm.name} disabled={!mutateOK} onChange={(e) => setCustomForm({ ...customForm, name: e.target.value })} /></label>
          <label>Base URL<input aria-label="Custom provider base URL" value={customForm.baseURL} disabled={!mutateOK} onChange={(e) => setCustomForm({ ...customForm, baseURL: e.target.value })} /></label>
          <label>
            API
            <select aria-label="Custom provider API" value={customForm.api} disabled={!mutateOK} onChange={(e) => setCustomForm({ ...customForm, api: e.target.value })}>
              <option value="openai">openai</option>
              <option value="anthropic">anthropic</option>
            </select>
          </label>
          <label>Models (comma-separated)<input aria-label="Custom provider models" value={customForm.models} disabled={!mutateOK} onChange={(e) => setCustomForm({ ...customForm, models: e.target.value })} /></label>
          <Button
            type="button"
            disabled={!mutateOK || !customForm.name.trim() || !customForm.baseURL.trim()}
            onClick={() => {
              const models = customForm.models.split(",").map((s) => s.trim()).filter(Boolean);
              void upsertCustomProvider({
                name: customForm.name.trim(),
                baseURL: customForm.baseURL.trim(),
                api: customForm.api,
                models,
              })
                .then(() => listCustomProviders())
                .then((r) => { setCustomProviders(r.providers || []); setCustomForm({ name: "", baseURL: "", api: "openai", models: "" }); })
                .then(() => refreshProviders())
                .catch((err: Error) => setError(err.message));
            }}
          >
            Save custom provider
          </Button>
        </fieldset>
      )}

      {permissionsCap && (
        <fieldset>
          <legend>Permission presets</legend>
          <ul>
            {permPresets.map((pr) => {
              const id = String(pr.id || pr.ID || "");
              const name = String(pr.name || pr.Name || id);
              const desc = String(pr.description || pr.Description || "");
              return <li key={id}><strong>{name}</strong>{desc ? ` — ${desc}` : ""}</li>;
            })}
          </ul>
          <label>Tool / permission<input aria-label="Permission name" value={permTool} onChange={(e) => setPermTool(e.target.value)} /></label>
          <label>Pattern<input aria-label="Permission pattern" value={permPattern} onChange={(e) => setPermPattern(e.target.value)} /></label>
          <label>
            Dry-run preset
            <select aria-label="Permission preset" value={permPreset} onChange={(e) => setPermPreset(e.target.value)}>
              <option value="">(effective)</option>
              {permPresets.map((pr) => {
                const id = String(pr.id || pr.ID || "");
                return <option key={id} value={id}>{String(pr.name || pr.Name || id)}</option>;
              })}
            </select>
          </label>
          <Button
            type="button"
            onClick={() => {
              void explainPermission(permTool, permPattern || "*", permPreset)
                .then((ex) => setPermExplain(String(ex.Summary || ex.summary || JSON.stringify(ex, null, 2))))
                .catch((err: Error) => setError(err.message));
            }}
          >
            Explain
          </Button>
          {permExplain && <pre className="permission-explain-body" aria-label="Permission explanation">{permExplain}</pre>}
        </fieldset>
      )}

      {schedulerCap && (
        <fieldset>
          <legend>Scheduler presets</legend>
          <p className="muted">Shipped build-system presets with effective global selection provenance.</p>
          {schedPresets.map((sp) => {
            const id = String(sp.id || sp.ID || "");
            const name = String(sp.name || sp.Name || id);
            const on = schedEnabled.includes(id);
            return (
              <label key={id} className="exclude-item">
                <input
                  type="checkbox"
                  checked={on}
                  disabled={!mutateOK}
                  aria-label={`Scheduler preset ${name}`}
                  onChange={() => {
                    setSchedEnabled((old) => (on ? old.filter((x) => x !== id) : [...old, id]));
                  }}
                />
                {name}
                <span className="muted">{String(sp.rationale || sp.Rationale || "")}</span>
              </label>
            );
          })}
          <Button
            type="button"
            disabled={!mutateOK}
            onClick={() => {
              void applySchedulerPresets(schedEnabled)
                .then(() => fetchSchedulerPresets())
                .then((r) => setSchedEnabled([...(r.global?.presets || [])]))
                .catch((err: Error) => setError(err.message));
            }}
          >
            Apply scheduler presets
          </Button>
          <p className="muted">Enabled: {schedEnabled.length ? schedEnabled.join(", ") : "(none)"}</p>
        </fieldset>
      )}

      {configFilesCap && (
        <fieldset>
          <legend>Config sources</legend>
          <p className="muted">Typed inspection of global/project config surfaces (not a raw file editor).</p>
          <ul>
            {configSources.map((src, i) => (
              <li key={`${src.scope}-${src.slot || src.kind}-${src.display}-${i}`}>
                <strong>{src.label || src.slot || src.kind}</strong>
                {" · "}
                <span className="muted">{src.scope}</span>
                {" · "}
                <code>{src.display}</code>
                {src.exists ? " · exists" : " · missing"}
              </li>
            ))}
          </ul>
        </fieldset>
      )}

      <fieldset>
        <legend>Appearance</legend>
        <SelectField
          label="Color scheme"
          value={appearance}
          values={[...appearanceValues]}
          onChange={(v) => onAppearance(v as Appearance)}
        />
        <p className="muted">Local to this browser. Auto follows the system preference.</p>
      </fieldset>

      {!settingsCap ? (
        <CapabilityUnavailable name="Saved defaults" />
      ) : loading ? (
        <LoadingState label="Loading settings…" />
      ) : (
        <form className="settings-form" onSubmit={(e) => void save(e)}>
          <fieldset>
            <legend>Runtime defaults</legend>
            <p className="muted">Persisted to ~/.strike/config. Empty leaves the stored value unchanged on save.</p>
            <TextField label="Provider" value={defProvider} onChange={setDefProvider} placeholder={status.provider || "provider id"} />
            <TextField label="Model" value={defModel} onChange={setDefModel} placeholder={status.model || "model id"} />
            <SelectField label="Agent" value={defAgent} values={["", ...agents]} onChange={setDefAgent} />
            <SelectField label="Effort" value={defEffort} values={effortValues} onChange={setDefEffort} />
            <SelectField label="Permission mode" value={defMode} values={modeValues} onChange={setDefMode} />
            <Button type="button" disabled={saving || !status.provider} onClick={() => void saveRuntimeDefaults()}>
              Use current runtime
            </Button>
          </fieldset>

          <fieldset>
            <legend>Sandbox default</legend>
            <SelectField label="Sandbox" value={sandbox} values={sandboxValues} onChange={setSandbox} />
            <p className="muted">OS process isolation for bash (off | read-only | workspace-write).</p>
          </fieldset>

          <fieldset>
            <legend>Behavior dials</legend>
            <SelectField label="Notify" value={notify} values={notifyValues} onChange={setNotify} />
            <SelectField label="Lean code" value={leanCode} values={leanCodeValues} onChange={setLeanCode} />
          </fieldset>

          <fieldset>
            <legend>Permission auto-approve</legend>
            <SelectField label="Countdown seconds" value={autoSecs} values={autoApproveValues} onChange={setAutoSecs} />
            <SelectField label="Max child depth" value={maxChildDepth} values={maxChildDepthValues} onChange={setMaxChildDepth} />
            <div className="exclude-list" role="group" aria-label="Auto-approve exclude">
              <span className="exclude-label">Never auto-approve</span>
              {excludeChoices.map((name) => (
                <label key={name} className="exclude-item">
                  <input
                    type="checkbox"
                    checked={exclude.includes(name)}
                    onChange={() => toggleExclude(name)}
                    aria-label={`Exclude ${name}`}
                  />
                  {name}
                </label>
              ))}
            </div>
          </fieldset>

          <fieldset>
            <legend>Compaction</legend>
            <SelectField label="Strategy" value={compStrategy} values={compactionStrategyValues} onChange={setCompStrategy} />
            <TextField label="Compaction model" value={compModel} onChange={setCompModel} placeholder="model id (− clears)" />
            <SelectField label="Threshold" value={compThreshold} values={thresholdValues} onChange={setCompThreshold} />
            <SelectField label="Buffer" value={compBuffer} values={bufferValues} onChange={setCompBuffer} />
            <SelectField label="Keep user turns" value={keepUserTurns} values={keepTurnsValues} onChange={setKeepUserTurns} />
            <TextField label="Prune protect tools" value={pruneProtectTools} onChange={setPruneProtectTools} placeholder="comma-separated (− clears)" />
          </fieldset>

          {error && <p className="settings-error" role="alert">{error}</p>}
          <div className="dialog-actions">
            <Button type="button" onClick={onClose}>Close</Button>
            <Button type="submit" variant="primary" disabled={saving}>{saving ? "Saving…" : "Save settings"}</Button>
          </div>
        </form>
      )}

      <fieldset>
        <legend>Support</legend>
        <p className="muted">Export a redacted prompt/config diagnostic bundle (same scrubbing as TUI <code>/diag</code>).</p>
        <Button type="button" disabled={!Boolean(boot?.capabilities.diag && isLive)} aria-label="Download diagnostics" onClick={() => void downloadDiagnostics(rootID || "").catch((error) => window.alert((error as Error).message))}>Download diagnostics</Button>
        {!boot?.capabilities.diag && <p className="muted">Unavailable on this host (no live engine).</p>}
      </fieldset>

      {error && !settingsCap && <p className="settings-error" role="alert">{error}</p>}
    </Dialog>
  );
}
