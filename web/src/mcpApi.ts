import { request } from "./api";

export type MCPServerStatus = {
  name: string;
  command?: string;
  transport?: string;
  state: string;
  toolCount: number;
  error?: string;
  tools?: string[];
};

export const listMCP = () => request<{ servers: MCPServerStatus[] }>("/v1/mcp");

export const retryMCP = (name?: string) =>
  request<{ ok: boolean }>("/v1/mcp/retry", {
    method: "POST",
    body: JSON.stringify(name ? { name } : {}),
  });

export const disableMCP = (name: string) =>
  request<{ ok: boolean }>("/v1/mcp/disable", {
    method: "POST",
    body: JSON.stringify({ name }),
  });
