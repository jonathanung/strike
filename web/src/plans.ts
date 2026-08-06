import { request } from "./api";

export type PlanMeta = {
  ID?: string; id?: string;
  OwnerRoot?: string; ownerRoot?: string;
  Title?: string; title?: string;
  Status?: string; status?: string;
  Version?: number; version?: number;
  SectionCount?: number; sectionCount?: number;
  CreatedAt?: string; createdAt?: string;
  UpdatedAt?: string; updatedAt?: string;
};

export type PlanSection = {
  ID?: string; id?: string;
  Title?: string; title?: string;
  Body?: string; body?: string;
  DelegateStatus?: string; delegateStatus?: string;
  DelegateChildID?: string; delegateChildID?: string;
  DelegateChildName?: string; delegateChildName?: string;
  DelegateDetail?: string; delegateDetail?: string;
};

export type Plan = PlanMeta & {
  Sections?: PlanSection[]; sections?: PlanSection[];
};

export const planID = (p: PlanMeta) => p.ID || p.id || "";
export const planTitle = (p: PlanMeta) => p.Title || p.title || "Untitled plan";
export const planStatus = (p: PlanMeta) => p.Status || p.status || "draft";
export const planVersion = (p: PlanMeta) => p.Version ?? p.version ?? 0;
export const planOwner = (p: PlanMeta) => p.OwnerRoot || p.ownerRoot || "";
export const planSections = (p: Plan) => p.Sections || p.sections || [];
export const sectionID = (s: PlanSection) => s.ID || s.id || "";
export const sectionTitle = (s: PlanSection) => s.Title || s.title || "";
export const sectionBody = (s: PlanSection) => s.Body || s.body || "";

export const listPlans = () => request<{ plans: PlanMeta[] }>("/v1/plans");
export const getPlan = (id: string) => request<Plan>(`/v1/plans/${encodeURIComponent(id)}`);
export const createPlan = (ownerRoot: string, title: string, sections?: { title: string; body?: string }[]) =>
  request<Plan>("/v1/plans", { method: "POST", body: JSON.stringify({ ownerRoot, title, sections }) });
export const updatePlanTitle = (id: string, ownerRoot: string, title: string, expectedVersion: number) =>
  request<Plan>(`/v1/plans/${encodeURIComponent(id)}`, {
    method: "PATCH", body: JSON.stringify({ ownerRoot, title, expectedVersion }),
  });
export const updatePlanSection = (
  id: string, sectionId: string, ownerRoot: string,
  patch: { title?: string; body?: string }, expectedVersion: number,
) => request<Plan>(`/v1/plans/${encodeURIComponent(id)}/sections/${encodeURIComponent(sectionId)}`, {
  method: "PATCH", body: JSON.stringify({ ownerRoot, ...patch, expectedVersion }),
});
export const addPlanSection = (id: string, ownerRoot: string, title: string, body: string, expectedVersion: number) =>
  request<Plan>(`/v1/plans/${encodeURIComponent(id)}/sections`, {
    method: "POST", body: JSON.stringify({ ownerRoot, title, body, expectedVersion }),
  });
export const setPlanStatus = (id: string, ownerRoot: string, status: string, expectedVersion: number) =>
  request<Plan>(`/v1/plans/${encodeURIComponent(id)}/status`, {
    method: "POST", body: JSON.stringify({ ownerRoot, status, expectedVersion }),
  });
export const reopenPlan = (id: string, ownerRoot: string, expectedVersion: number) =>
  request<Plan>(`/v1/plans/${encodeURIComponent(id)}/reopen`, {
    method: "POST", body: JSON.stringify({ ownerRoot, expectedVersion }),
  });
