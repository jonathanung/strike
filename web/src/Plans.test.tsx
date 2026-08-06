import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { PlansPanel } from "./Plans";

const response = (body: unknown, status = 200) =>
  Promise.resolve(new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }));

describe("PlansPanel", () => {
  afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

  it("shows unavailable state when capability is absent", () => {
    render(<PlansPanel available={false} live={false} rootID="" />);
    expect(screen.getByRole("status")).toHaveTextContent("Plans unavailable");
  });

  it("lists plans and opens detail with sections", async () => {
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/v1/plans") && (!init || !init.method || init.method === "GET")) {
        return response({ plans: [{ ID: "p1", Title: "Ship it", Status: "draft", Version: 1, SectionCount: 1, OwnerRoot: "root-a" }] });
      }
      if (url.includes("/v1/plans/p1") && (!init || !init.method || init.method === "GET")) {
        return response({
          ID: "p1", Title: "Ship it", Status: "draft", Version: 1, OwnerRoot: "root-a",
          Sections: [{ ID: "s1", Title: "API", Body: "REST endpoints" }],
        });
      }
      return response({ ok: true });
    }));
    render(<PlansPanel available live rootID="root-a" />);
    expect(await screen.findByText("Ship it")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Open" }));
    expect(await screen.findByText("API")).toBeInTheDocument();
    expect(screen.getByText("REST endpoints")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Approve" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Edit title" })).toBeInTheDocument();
  });

  it("is read-only for non-owner roots", async () => {
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/v1/plans")) {
        return response({ plans: [{ ID: "p1", Title: "Other", Status: "draft", Version: 1, OwnerRoot: "root-a" }] });
      }
      if (url.includes("/v1/plans/p1")) {
        return response({ ID: "p1", Title: "Other", Status: "draft", Version: 1, OwnerRoot: "root-a", Sections: [] });
      }
      return response({ ok: true });
    }));
    render(<PlansPanel available live rootID="root-b" />);
    fireEvent.click(await screen.findByRole("button", { name: "Open" }));
    expect(await screen.findByRole("status")).toHaveTextContent("owned by another root");
    expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit title" })).not.toBeInTheDocument();
  });

  it("creates a plan for the live root", async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/v1/plans") && init?.method === "POST") {
        return response({ ID: "p2", Title: "New plan", Status: "draft", Version: 1, OwnerRoot: "root-a", Sections: [] }, 201);
      }
      if (url.endsWith("/v1/plans")) {
        return response({ plans: [] });
      }
      return response({ ok: true });
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<PlansPanel available live rootID="root-a" />);
    await screen.findByText("No project plans.");
    fireEvent.click(screen.getByRole("button", { name: "New" }));
    fireEvent.change(screen.getByLabelText("New plan title"), { target: { value: "New plan" } });
    fireEvent.click(screen.getByRole("button", { name: "Create" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith(
      "/v1/plans",
      expect.objectContaining({ method: "POST", body: expect.stringContaining('"ownerRoot":"root-a"') }),
    ));
    expect(await screen.findByText("New plan")).toBeInTheDocument();
  });

  it("approves a plan via status API", async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/v1/plans") && (!init || !init.method || init.method === "GET")) {
        return response({ plans: [{ ID: "p1", Title: "Ship", Status: "draft", Version: 1, OwnerRoot: "root-a" }] });
      }
      if (url.includes("/status") && init?.method === "POST") {
        return response({ ID: "p1", Title: "Ship", Status: "approved", Version: 2, OwnerRoot: "root-a", Sections: [] });
      }
      if (url.includes("/v1/plans/p1")) {
        return response({ ID: "p1", Title: "Ship", Status: "draft", Version: 1, OwnerRoot: "root-a", Sections: [] });
      }
      return response({ ok: true });
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<PlansPanel available live rootID="root-a" />);
    fireEvent.click(await screen.findByRole("button", { name: "Open" }));
    fireEvent.click(await screen.findByRole("button", { name: "Approve" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining("/v1/plans/p1/status"),
      expect.objectContaining({ method: "POST", body: expect.stringContaining('"status":"approved"') }),
    ));
    expect(await screen.findByText("approved")).toBeInTheDocument();
  });
});
