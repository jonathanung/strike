import { request } from "./api";

export type WorkflowPermission = { permission: string; pattern?: string; action: string };
export type WorkflowPhaseSummary = {
  name: string; description?: string; agent?: string; gate?: string;
  gateCommand?: string; permissions?: WorkflowPermission[];
};
export type WorkflowSummary = {
  name: string; description?: string; source?: string; fingerprint?: string;
  path?: string; valid: boolean; validationError?: string; phases?: WorkflowPhaseSummary[];
};
export type WorkflowPhaseDocument = {
  name: string; description?: string; agent?: string; context?: string;
  permissions?: WorkflowPermission[]; gate?: string; gateCommand?: string;
};
export type WorkflowDocument = {
  schemaVersion?: number; name: string; description?: string; phases: WorkflowPhaseDocument[];
};
export type WorkflowPhaseDraftReview = {
  name: string; description?: string; agent?: string; context?: string;
  gate?: string; gateCommand?: string; checkHighlighted?: boolean;
  permissions?: WorkflowPermission[]; widening?: WorkflowPermission[];
};
export type WorkflowDraftReview = {
  name: string; description?: string; sourceLabel?: string; valid: boolean;
  validationError?: string; fingerprint?: string; hasChecks?: boolean; hasWidening?: boolean;
  phases?: WorkflowPhaseDraftReview[]; canonicalJson?: string;
};

export const listWorkflows = () => request<{ workflows: WorkflowSummary[] }>("/v1/workflows");
export const getWorkflow = (name: string) => request<WorkflowSummary>(`/v1/workflows/${encodeURIComponent(name)}`);
export const getWorkflowDocument = (name: string) => request<WorkflowDocument>(`/v1/workflows/${encodeURIComponent(name)}/document`);
export const scaffoldWorkflow = (name: string) => request<WorkflowDocument>("/v1/workflows/scaffold", { method: "POST", body: JSON.stringify({ name }) });
export const validateWorkflow = (document: WorkflowDocument) => request<{ ok: boolean; error?: string }>("/v1/workflows/validate", { method: "POST", body: JSON.stringify({ document }) });
export const formatWorkflow = (document: WorkflowDocument) => request<{ json: string }>("/v1/workflows/format", { method: "POST", body: JSON.stringify({ document }) });
export const phaseGrants = (document: WorkflowDocument, phaseIndex: number) => request<{ grants: WorkflowPermission[] }>("/v1/workflows/phase-grants", { method: "POST", body: JSON.stringify({ document, phaseIndex }) });
export const saveWorkflow = (document: WorkflowDocument, scope: string, force: boolean) => request<{ path: string; activated: boolean }>("/v1/workflows/save", { method: "POST", body: JSON.stringify({ document, scope, force }) });
export const startWorkflow = (name: string, rootID?: string) => {
  const qs = rootID ? `?root=${encodeURIComponent(rootID)}` : "";
  return request<{ ok: boolean }>(`/v1/workflows/${encodeURIComponent(name)}/start${qs}`, { method: "POST", body: JSON.stringify({ confirm: true }) });
};
export const stopWorkflow = (rootID?: string) => {
  const qs = rootID ? `?root=${encodeURIComponent(rootID)}` : "";
  return request<{ ok: boolean }>(`/v1/workflows/stop${qs}`, { method: "POST", body: JSON.stringify({}) });
};
export const reviewDraft = (json: string) => request<WorkflowDraftReview>("/v1/workflow-drafts/review", { method: "POST", body: JSON.stringify({ json }) });
export const saveDraft = (json: string, scope: string, confirm: boolean, force: boolean) =>
  request<{ path: string; activated: boolean }>("/v1/workflow-drafts/save", { method: "POST", body: JSON.stringify({ json, scope, confirm, force }) });

export const emptyPhase = (): WorkflowPhaseDocument => ({ name: "phase-1", gate: "agent", permissions: [] });
export const cloneDoc = (doc: WorkflowDocument): WorkflowDocument => JSON.parse(JSON.stringify(doc));
