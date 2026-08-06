import { FormEvent, useEffect, useRef, useState } from "react";
import { downloadDiagnostics, request } from "./api";
import type { Bootstrap, Status } from "./types";

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

function CapabilityUnavailable({ name }: { name: string }) {
  return (
    <section className="unavailable" role="status">
      <strong>{name} unavailable</strong>
      <p>The configured host did not provide this capability. No action was attempted.</p>
    </section>
  );
}

export function SettingsDialog({
  boot, status, providers, onClose, rootID, isLive,
}: {
  boot?: Bootstrap; status: Status; providers: string[]; onClose: () => void; rootID?: string; isLive?: boolean;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  const [provider, setProvider] = useState(String(status.provider || providers[0] || ""));
  const [key, setKey] = useState("");
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [appearance, setAppearance] = useState<Appearance>(() => loadAppearance());

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
  const agents = boot?.agents?.map((a) => a.name) || [];

  useEffect(() => { ref.current?.showModal(); }, []);

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

  return (
    <dialog ref={ref} className="settings-dialog wide" aria-labelledby="settings-title" onClose={onClose}>
      <div className="dialog-rule" />
      <h2 id="settings-title">Workspace settings</h2>

      {boot?.capabilities.auth ? (
        <fieldset>
          <legend>Provider authentication</legend>
          <label>
            Provider
            <select value={provider} onChange={(e) => setProvider(e.target.value)} aria-label="Auth provider">
              {providers.map((name) => <option key={name}>{name}</option>)}
            </select>
          </label>
          <label>
            API key
            <input value={key} onChange={(e) => setKey(e.target.value)} placeholder="Stored locally by strike" aria-label="API key" />
          </label>
          <button
            type="button"
            disabled={!provider || !key}
            onClick={() => void request("/v1/auth/key", { method: "POST", body: JSON.stringify({ provider, key }) }).then(() => setKey("")).catch((err: Error) => setError(err.message))}
          >
            Save key
          </button>
        </fieldset>
      ) : (
        <CapabilityUnavailable name="Provider authentication" />
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
        <section className="unavailable" role="status"><strong>Loading settings…</strong></section>
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
            <button type="button" disabled={saving || !status.provider} onClick={() => void saveRuntimeDefaults()}>
              Use current runtime
            </button>
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
            <button type="button" onClick={onClose}>Close</button>
            <button type="submit" disabled={saving}>{saving ? "Saving…" : "Save settings"}</button>
          </div>
        </form>
      )}

      
      <fieldset>
        <legend>Support</legend>
        <p className="muted">Export a redacted prompt/config diagnostic bundle (same scrubbing as TUI <code>/diag</code>).</p>
        <button type="button" disabled={!Boolean(boot?.capabilities.diag && isLive)} aria-label="Download diagnostics" onClick={() => void downloadDiagnostics(rootID || "").catch((error) => window.alert((error as Error).message))}>Download diagnostics</button>
        {!boot?.capabilities.diag && <p className="muted">Unavailable on this host (no live engine).</p>}
      </fieldset>

      {!settingsCap && (
        <div className="dialog-actions">
          <button type="button" onClick={onClose}>Close</button>
        </div>
      )}
      {settingsCap && loading && (
        <div className="dialog-actions">
          <button type="button" onClick={onClose}>Close</button>
        </div>
      )}
      {error && !settingsCap && <p className="settings-error" role="alert">{error}</p>}
    </dialog>
  );
}
