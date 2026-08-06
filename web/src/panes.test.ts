import { describe, expect, it } from "vitest";
import { resolveFrom } from "./panes";

describe("resolveFrom", () => {
  const feeds = {
    "session.summary": { model: "echo", cwd: "/work", agent: "build" },
    usage: { contextUsed: 100, contextLimit: 200 },
    clock: { unix: 1 },
  };

  it("resolves nested session.summary fields", () => {
    expect(resolveFrom(feeds, "session.summary.model")).toBe("echo");
    expect(resolveFrom(feeds, "session.summary.cwd")).toBe("/work");
  });

  it("resolves single-segment feeds", () => {
    expect(resolveFrom(feeds, "usage.contextUsed")).toBe("100");
    expect(resolveFrom(feeds, "clock.unix")).toBe("1");
  });

  it("returns empty for missing paths", () => {
    expect(resolveFrom(feeds, "session.summary.missing")).toBe("");
    expect(resolveFrom(undefined, "x")).toBe("");
    expect(resolveFrom(feeds, "")).toBe("");
  });
});
