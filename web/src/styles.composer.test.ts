import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const css = readFileSync(path.join(path.dirname(fileURLToPath(import.meta.url)), "styles.css"), "utf8");

describe("composer CSS scoping", () => {
  it("does not restyle completion rows or footer actions as Send", () => {
    expect(css).not.toMatch(/\.composer\s*>\s*div\s*\{/);
    expect(css).not.toMatch(/\.composer\s+button\s*\{/);
    expect(css).toMatch(/\.composer-bar\s*\{/);
    expect(css).toMatch(/\.composer-send\s*\{/);
    expect(css).toMatch(/\.composer-secondary\s*\{/);
    expect(css).toMatch(/\.completion\s*\{[^}]*flex-direction:\s*column/);
    expect(css).toMatch(/\.completion button\s*\{[^}]*text-transform:\s*none/);
  });
});
