/**
 * Case-insensitive filesystems (macOS APFS) collapse Team.tsx vs team.ts into one
 * path. Vite then resolves lazy import("./Team") to the helper module, so the
 * cockpit mounts undefined and blanks /attach (#1128).
 */
import { readdirSync } from "node:fs";
import { join, relative, sep } from "node:path";
import { describe, expect, it } from "vitest";

function listFiles(dir: string): string[] {
  const out: string[] = [];
  for (const ent of readdirSync(dir, { withFileTypes: true })) {
    if (ent.name === "node_modules") continue;
    const p = join(dir, ent.name);
    if (ent.isDirectory()) out.push(...listFiles(p));
    else out.push(p);
  }
  return out;
}

describe("web/src path uniqueness", () => {
  it("has no two files that differ only by case", () => {
    const root = join(process.cwd(), "src");
    const files = listFiles(root).map((f) => relative(root, f).split(sep).join("/"));
    const seen = new Map<string, string>();
    const collisions: string[] = [];
    for (const f of files) {
      const key = f.toLowerCase();
      const prev = seen.get(key);
      if (prev) collisions.push(`${prev} vs ${f}`);
      else seen.set(key, f);
    }
    expect(collisions).toEqual([]);
  });
});
