import { describe, expect, it } from "vitest";
import {
  deepLinkWorkspaceID,
  parseDeepLink,
  resolveDeepLink,
  serializeDeepLink,
} from "./deepLink";
import type { Capabilities } from "./types";

const caps: Capabilities = {
  files: true,
  plans: true,
  goals: true,
  panes: true,
  mcp: true,
  timeline: true,
  live: true,
};

describe("deepLink", () => {
  it("parses root/session with root winning", () => {
    const p = parseDeepLink("?root=r1&session=s1&mode=project&surface=plans&entity=p9");
    expect(p.workspaceID).toBe("r1");
    expect(p.raw.session).toBe("s1");
    expect(p.raw.mode).toBe("project");
    expect(p.raw.surface).toBe("plans");
    expect(p.raw.entity).toBe("p9");
    expect(p.modePresent).toBe(true);
  });

  it("defaults mode to chat when absent or invalid", () => {
    expect(parseDeepLink("?session=s1").raw.mode).toBe("chat");
    expect(parseDeepLink("?mode=nope").raw.mode).toBe("chat");
    expect(resolveDeepLink("?mode=nope", caps).mode).toBe("chat");
  });

  it("resolves mode=project&surface=plans after bootstrap", () => {
    const r = resolveDeepLink("?root=live1&mode=project&surface=plans&entity=plan-a", caps);
    expect(r.workspaceID).toBe("live1");
    expect(r.mode).toBe("project");
    expect(r.surface).toBe("plans");
    expect(r.entity).toBe("plan-a");
    expect(r.openDrawer).toBe(true);
    expect(r.unavailable).toBeUndefined();
  });

  it("preserves root/session serialization and omits chat defaults", () => {
    const qs = serializeDeepLink(
      { root: "r1", mode: "project", surface: "plans", entity: "e1" },
      "?token=secret&foo=bar",
    );
    const q = new URLSearchParams(qs);
    expect(q.get("token")).toBeNull();
    expect(q.get("foo")).toBe("bar");
    expect(q.get("root")).toBe("r1");
    expect(q.get("mode")).toBe("project");
    expect(q.get("surface")).toBe("plans");
    expect(q.get("entity")).toBe("e1");

    const chat = serializeDeepLink({ mode: "chat", surface: "" }, "?root=r1");
    expect(chat).toBe("root=r1");
  });

  it("unknown surface does not break workspace id", () => {
    const r = resolveDeepLink("?root=r9&mode=code&surface=not-a-surface", caps);
    expect(r.workspaceID).toBe("r9");
    expect(r.mode).toBe("code");
    expect(r.surface).toBe("files");
  });

  it("capability-false deep link yields unavailable state", () => {
    const r = resolveDeepLink("?mode=project&surface=plans", { plans: false });
    expect(r.mode).toBe("project");
    expect(r.surface).toBe("plans");
    expect(r.unavailable?.id).toBe("plans");
  });

  it("path implies code mode when mode omitted", () => {
    const r = resolveDeepLink("?root=r1&path=internal/frontend/server/api.go", caps);
    expect(r.mode).toBe("code");
    expect(r.surface).toBe("files");
    expect(r.path).toBe("internal/frontend/server/api.go");
  });

  it("agent implies team mode", () => {
    const r = resolveDeepLink("?agent=child-9", caps);
    expect(r.mode).toBe("team");
    expect(r.surface).toBe("roster");
    expect(r.agent).toBe("child-9");
  });

  it("deepLinkWorkspaceID reads root or session", () => {
    expect(deepLinkWorkspaceID("?session=s")).toBe("s");
    expect(deepLinkWorkspaceID("?root=r&session=s")).toBe("r");
    expect(deepLinkWorkspaceID("")).toBe("");
  });

  it("ignores unknown query keys (forward compatible)", () => {
    const p = parseDeepLink("?root=r1&future=1&mode=ops&surface=mcp");
    expect(p.workspaceID).toBe("r1");
    expect(p.raw.mode).toBe("ops");
    expect(p.raw.surface).toBe("mcp");
  });
});
