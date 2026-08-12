import { request } from "./api";

export type GoalCriterion = {
  description: string;
  check: string;
  satisfied: boolean;
};

export type Goal = {
  id: string;
  description: string;
  criteria: GoalCriterion[];
  status: string;
  maxIterations: number;
  maxCostUsd: number;
  allowedTools?: string[];
  costUsd: number;
  lastIteration: number;
  failReason?: string;
  createdAt?: string;
};

export type GoalIteration = {
  n: number;
  plan?: string;
  stateHash?: string;
  costUsd: number;
  summary?: string;
};

export type GoalSetInput = {
  description: string;
  criteria: string[];
  maxIterations?: number;
  maxCostUsd?: number;
  maxWallClockS?: number;
  maxNoProgressIters?: number;
  allowedTools?: string[];
};

export const listGoals = () => request<{ goals: Goal[] }>("/v1/goals");
export const getGoal = (id: string) => request<Goal>(`/v1/goals/${encodeURIComponent(id)}`);
export const setGoal = (body: GoalSetInput) =>
  request<Goal>("/v1/goals", { method: "POST", body: JSON.stringify(body) });
export const runGoal = (id: string) =>
  request<Goal>(`/v1/goals/${encodeURIComponent(id)}/run`, { method: "POST", body: "{}" });
export const pauseGoal = (id: string) =>
  request<Goal>(`/v1/goals/${encodeURIComponent(id)}/pause`, { method: "POST", body: "{}" });
export const resumeGoal = (id: string) =>
  request<Goal>(`/v1/goals/${encodeURIComponent(id)}/resume`, { method: "POST", body: "{}" });
export const abortGoal = (id: string) =>
  request<Goal>(`/v1/goals/${encodeURIComponent(id)}/abort`, { method: "POST", body: "{}" });
export const goalLog = (id: string, iter?: number) => {
  const qs = iter && iter > 0 ? `?iter=${iter}` : "";
  return request<{ iterations: GoalIteration[] }>(`/v1/goals/${encodeURIComponent(id)}/log${qs}`);
};

/** Statuses that still accept pause/resume/abort/run. */
export function isControllable(status: string): boolean {
  return status === "pending" || status === "active" || status === "paused";
}

export function canRun(status: string): boolean {
  return status === "pending" || status === "paused" || status === "active";
}

export function canPause(status: string): boolean {
  return status === "active";
}

export function canResume(status: string): boolean {
  return status === "pending" || status === "paused";
}

export function canAbort(status: string): boolean {
  return isControllable(status);
}
