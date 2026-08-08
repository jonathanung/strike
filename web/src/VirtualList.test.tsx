import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { buildOffsets, estimateMountedCount, visibleRange, VirtualList } from "./VirtualList";

describe("visibleRange / offsets", () => {
  it("builds prefix offsets", () => {
    expect(buildOffsets([10, 20, 30])).toEqual([0, 10, 30, 60]);
  });

  it("returns full range when under maxMounted", () => {
    const heights = Array.from({ length: 20 }, () => 50);
    const offsets = buildOffsets(heights);
    const r = visibleRange(0, 200, 20, (i) => offsets[i], (i) => heights[i], 2, 120);
    expect(r).toEqual({ start: 0, end: 20 });
  });

  it("windows a long list around the viewport", () => {
    const n = 1000;
    const heights = Array.from({ length: n }, () => 100);
    const offsets = buildOffsets(heights);
    const r = visibleRange(5000, 400, n, (i) => offsets[i], (i) => heights[i], 2, 40);
    expect(r.end - r.start).toBeLessThanOrEqual(40);
    expect(r.start).toBeGreaterThan(0);
    expect(r.end).toBeLessThan(n);
    // Viewport at y=5000 with h=100 → index ~50
    expect(r.start).toBeLessThanOrEqual(50);
    expect(r.end).toBeGreaterThanOrEqual(50);
  });

  it("estimateMountedCount stays bounded", () => {
    expect(estimateMountedCount(10_000, 800, 96, 6, 120)).toBeLessThanOrEqual(120);
    expect(estimateMountedCount(10, 800, 96, 6, 120)).toBe(10);
  });
});

describe("VirtualList", () => {
  afterEach(() => cleanup());

  it("mounts a bounded window and keeps full data addressable by index attrs", () => {
    const items = Array.from({ length: 200 }, (_, i) => ({ id: `i${i}`, text: `row ${i}` }));
    const { container } = render(
      <div style={{ height: 300, overflow: "auto" }} data-testid="scroller">
        <VirtualList
          items={items}
          itemKey={(it) => it.id}
          estimateHeight={40}
          overscan={2}
          maxMounted={30}
          scrollRef={{ current: null }}
          renderItem={(it) => <div>{it.text}</div>}
          aria-label="Test list"
        />
      </div>,
    );
    // Without a real scroll parent measurement in jsdom, list may mount all when
    // under max or a window — assert mounted count attribute is present and bounded.
    const root = container.querySelector(".virtual-list") as HTMLElement;
    expect(root).toBeTruthy();
    expect(root.getAttribute("aria-label")).toBe("Test list");
    const mounted = Number(root.getAttribute("data-virtual-mounted") || "0");
    const total = Number(root.getAttribute("data-virtual-total") || "0");
    expect(total).toBe(200);
    expect(mounted).toBeGreaterThan(0);
    expect(mounted).toBeLessThanOrEqual(30);
  });

  it("renders visible labels for keyboard/AT without flooding all rows", () => {
    const items = Array.from({ length: 50 }, (_, i) => ({ id: `k${i}`, text: `cell-${i}` }));
    render(
      <VirtualList
        items={items}
        itemKey={(it) => it.id}
        estimateHeight={48}
        maxMounted={20}
        renderItem={(it) => <article aria-label={it.text}>{it.text}</article>}
      />,
    );
    const mounted = screen.getAllByText(/cell-/);
    expect(mounted.length).toBeLessThanOrEqual(20);
    expect(mounted.length).toBeGreaterThan(0);
  });
});
