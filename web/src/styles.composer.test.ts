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
    expect(css).toMatch(/\.composer-field\s*\{/);
    const completionBlock = css.match(/\.completion\s*\{[^}]*\}/)?.[0] ?? "";
    expect(completionBlock).toMatch(/flex-direction:\s*column\s*;/);
    expect(completionBlock).not.toMatch(/column-reverse/);
    expect(completionBlock).toMatch(/--completion-max-rows:\s*6/);
    expect(completionBlock).toMatch(/max-height:\s*calc\(var\(--completion-max-rows\)/);
    expect(completionBlock).not.toMatch(/position:\s*absolute/);
    expect(completionBlock).not.toMatch(/bottom:\s*100%/);
    expect(css).not.toMatch(/\.completion button\s*\{[^}]*grid-template-columns:\s*150px/);
    expect(css).toMatch(/\.completion button\s*\{[^}]*display:\s*flex/);
    expect(css).toMatch(/\.completion button\s*\{[^}]*text-transform:\s*none/);
    expect(css).not.toMatch(/grid-template-columns:\s*150px/);
  });
});
