import { cleanup, render, screen, fireEvent, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { CodeExplorer } from "./CodeExplorer";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function mockFetch() {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url =
        typeof input === "string"
          ? input
          : input instanceof URL
            ? input.toString()
            : (input as Request).url || String(input);
      if (url.includes("/v1/files/search")) {
        return { ok: true, status: 200, json: async () => ({ paths: ["pkg/a.go", "README.md"] }) };
      }
      // Match list before single-file: "/v1/files" is a prefix of nothing else here,
      // but "/v1/file" is a prefix of "/v1/files" — order matters.
      if (url.includes("/v1/files")) {
        return {
          ok: true,
          status: 200,
          json: async () => ({
            entries: [
              { Name: "pkg", IsDir: true },
              { Name: "README.md", IsDir: false },
            ],
          }),
        };
      }
      if (url.includes("/v1/file")) {
        return {
          ok: true,
          status: 200,
          json: async () => ({ Path: "README.md", Content: "# Hello\n\nWorld", Skip: false }),
        };
      }
      return { ok: false, status: 404, json: async () => ({ error: `not found: ${url}` }) };
    }),
  );
}

describe("CodeExplorer", () => {
  beforeEach(() => {
    mockFetch();
  });

  it("lists directory and opens a file with markdown preview", async () => {
    render(<CodeExplorer available rootID="r1" />);
    fireEvent.click(screen.getByRole("tab", { name: "Browse" }));
    await waitFor(() => expect(screen.getByText("README.md")).toBeInTheDocument());
    fireEvent.click(screen.getByText("README.md"));
    await waitFor(() => expect(screen.getByLabelText("File viewer")).toBeInTheDocument());
    await waitFor(() => expect(screen.getByText("Hello")).toBeInTheDocument());
    fireEvent.click(screen.getByRole("button", { name: "Raw" }));
    expect(screen.getByText(/# Hello/)).toBeInTheDocument();
  });

  it("shows unavailable when capability missing", () => {
    render(<CodeExplorer available={false} />);
    expect(screen.getByText(/unavailable/i)).toBeInTheDocument();
  });

  it("opens changed files into the viewer", async () => {
    render(
      <CodeExplorer
        available
        rootID="r1"
        changedFiles={[{ path: "README.md", added: 1, deleted: 0, diff: "+x" }]}
      />,
    );
    fireEvent.click(screen.getByRole("tab", { name: /Changed/ }));
    fireEvent.click(screen.getByText("README.md"));
    await waitFor(() => expect(screen.getByRole("tab", { name: "Browse" })).toHaveAttribute("aria-selected", "true"));
  });
});
