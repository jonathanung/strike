/**
 * Windowed list virtualization without external deps (WEBUI.20).
 * Renders only the visible slice + overscan; full data stays in memory.
 * Supports scroll-anchor restore when items are prepended (load-older).
 */
import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type ReactNode,
  type RefObject,
} from "react";

export type VirtualListProps<T> = {
  items: T[];
  /** Stable key per item. */
  itemKey: (item: T, index: number) => string;
  renderItem: (item: T, index: number) => ReactNode;
  /** Estimated row height in px before measurement. */
  estimateHeight?: number;
  overscan?: number;
  /** Scroll container; defaults to nearest overflow ancestor via internal wrapper. */
  scrollRef?: RefObject<HTMLElement | null>;
  className?: string;
  /** Accessible label for the list region. */
  "aria-label"?: string;
  /** Called when the user scrolls near the top (load-older hook). */
  onNearTop?: () => void;
  nearTopPx?: number;
  /** When true, stick scroll to bottom as items append (live stream). */
  stickToBottom?: boolean;
  /** Optional max DOM rows hard cap (safety). */
  maxMounted?: number;
};

type Range = { start: number; end: number };

const DEFAULT_ESTIMATE = 96;
const DEFAULT_OVERSCAN = 6;
const DEFAULT_MAX_MOUNTED = 120;

function clamp(n: number, lo: number, hi: number) {
  return Math.max(lo, Math.min(hi, n));
}

/**
 * Compute the visible index window for a scroll offset.
 * Exported for unit tests / fixtures.
 */
export function visibleRange(
  scrollTop: number,
  viewportHeight: number,
  count: number,
  getOffset: (index: number) => number,
  getSize: (index: number) => number,
  overscan: number,
  maxMounted: number,
): Range {
  if (count <= 0) return { start: 0, end: 0 };
  if (count <= maxMounted) return { start: 0, end: count };

  let start = 0;
  let low = 0;
  let high = count - 1;
  while (low <= high) {
    const mid = (low + high) >> 1;
    const offset = getOffset(mid);
    const size = getSize(mid);
    if (offset + size < scrollTop) low = mid + 1;
    else {
      start = mid;
      high = mid - 1;
    }
  }

  const viewEnd = scrollTop + viewportHeight;
  let end = start;
  while (end < count && getOffset(end) < viewEnd) end += 1;

  start = clamp(start - overscan, 0, count);
  end = clamp(end + overscan, 0, count);

  // Hard cap mounted rows around the viewport center if overscan explodes.
  if (end - start > maxMounted) {
    const mid = (start + end) >> 1;
    start = clamp(mid - Math.floor(maxMounted / 2), 0, count);
    end = clamp(start + maxMounted, 0, count);
    start = clamp(end - maxMounted, 0, count);
  }
  return { start, end };
}

/** Prefix-sum offsets from a height array. */
export function buildOffsets(heights: number[]): number[] {
  const offsets = new Array(heights.length + 1);
  offsets[0] = 0;
  for (let i = 0; i < heights.length; i++) offsets[i + 1] = offsets[i] + heights[i];
  return offsets;
}

export function VirtualList<T>({
  items,
  itemKey,
  renderItem,
  estimateHeight = DEFAULT_ESTIMATE,
  overscan = DEFAULT_OVERSCAN,
  scrollRef,
  className,
  "aria-label": ariaLabel,
  onNearTop,
  nearTopPx = 120,
  stickToBottom = false,
  maxMounted = DEFAULT_MAX_MOUNTED,
}: VirtualListProps<T>) {
  const localRef = useRef<HTMLDivElement>(null);
  const heightsRef = useRef<Map<string, number>>(new Map());
  const [scrollTop, setScrollTop] = useState(0);
  const [viewport, setViewport] = useState(600);
  const [version, setVersion] = useState(0);
  const prevCount = useRef(items.length);
  const prevFirstKey = useRef(items[0] ? itemKey(items[0], 0) : "");
  const anchorRef = useRef<{ key: string; offset: number } | null>(null);

  const getScrollEl = useCallback((): HTMLElement | null => {
    return scrollRef?.current ?? localRef.current;
  }, [scrollRef]);

  const heights = useMemo(() => {
    void version;
    return items.map((item, i) => {
      const key = itemKey(item, i);
      return heightsRef.current.get(key) ?? estimateHeight;
    });
  }, [items, itemKey, estimateHeight, version]);

  const offsets = useMemo(() => buildOffsets(heights), [heights]);
  const totalHeight = offsets[items.length] || 0;

  const getOffset = useCallback((i: number) => offsets[i] ?? 0, [offsets]);
  const getSize = useCallback((i: number) => heights[i] ?? estimateHeight, [heights, estimateHeight]);

  const range = useMemo(
    () => visibleRange(scrollTop, viewport, items.length, getOffset, getSize, overscan, maxMounted),
    [scrollTop, viewport, items.length, getOffset, getSize, overscan, maxMounted],
  );

  // Preserve scroll when items are prepended (load-older / backfill).
  useLayoutEffect(() => {
    const el = getScrollEl();
    if (!el) return;
    const firstKey = items[0] ? itemKey(items[0], 0) : "";
    const grewAtFront =
      items.length > prevCount.current &&
      firstKey &&
      firstKey !== prevFirstKey.current &&
      anchorRef.current;

    if (grewAtFront && anchorRef.current) {
      const anchorKey = anchorRef.current.key;
      const idx = items.findIndex((it, i) => itemKey(it, i) === anchorKey);
      if (idx >= 0) {
        const nextTop = getOffset(idx) - anchorRef.current.offset;
        el.scrollTop = Math.max(0, nextTop);
        setScrollTop(el.scrollTop);
      }
    } else if (stickToBottom && items.length >= prevCount.current) {
      el.scrollTop = el.scrollHeight;
      setScrollTop(el.scrollTop);
    }

    prevCount.current = items.length;
    prevFirstKey.current = firstKey;
  }, [items, itemKey, getOffset, getScrollEl, stickToBottom]);

  useEffect(() => {
    const el = getScrollEl();
    if (!el) return;

    const onScroll = () => {
      const top = el.scrollTop;
      setScrollTop(top);
      setViewport(el.clientHeight || 600);
      if (onNearTop && top < nearTopPx) onNearTop();

      // Track first visible item as scroll anchor.
      const r = visibleRange(top, el.clientHeight || 600, items.length, getOffset, getSize, 0, maxMounted);
      const idx = r.start;
      if (items[idx]) {
        const key = itemKey(items[idx], idx);
        anchorRef.current = { key, offset: getOffset(idx) - top };
      }
    };

    const ro = typeof ResizeObserver !== "undefined"
      ? new ResizeObserver(() => {
          setViewport(el.clientHeight || 600);
        })
      : undefined;
    ro?.observe(el);
    el.addEventListener("scroll", onScroll, { passive: true });
    onScroll();
    return () => {
      el.removeEventListener("scroll", onScroll);
      ro?.disconnect();
    };
  }, [getScrollEl, onNearTop, nearTopPx, items, itemKey, getOffset, getSize, maxMounted]);

  const measure = useCallback((key: string, node: HTMLElement | null) => {
    if (!node) return;
    const h = node.getBoundingClientRect().height;
    if (h <= 0) return;
    const prev = heightsRef.current.get(key);
    if (prev !== undefined && Math.abs(prev - h) < 1) return;
    heightsRef.current.set(key, h);
    setVersion((v) => v + 1);
  }, []);

  const slice = items.slice(range.start, range.end);
  const padTop = getOffset(range.start);
  const padBottom = Math.max(0, totalHeight - getOffset(range.end));

  const style: CSSProperties = scrollRef ? { position: "relative" } : {
    position: "relative",
    overflow: "auto",
    height: "100%",
    minHeight: 0,
  };

  return (
    <div
      ref={scrollRef ? undefined : localRef}
      className={className ? `virtual-list ${className}` : "virtual-list"}
      style={style}
      role="log"
      aria-label={ariaLabel}
      aria-relevant="additions"
      aria-busy={false}
      data-virtual-start={range.start}
      data-virtual-end={range.end}
      data-virtual-total={items.length}
      data-virtual-mounted={slice.length}
    >
      <div className="virtual-list-spacer" style={{ height: totalHeight, position: "relative" }} aria-hidden={false}>
        <div style={{ height: padTop }} aria-hidden="true" data-pad="top" />
        {slice.map((item, i) => {
          const index = range.start + i;
          const key = itemKey(item, index);
          return (
            <div
              key={key}
              data-virtual-index={index}
              ref={(node) => measure(key, node)}
            >
              {renderItem(item, index)}
            </div>
          );
        })}
        <div style={{ height: padBottom }} aria-hidden="true" data-pad="bottom" />
      </div>
    </div>
  );
}

/** How many DOM rows a VirtualList would mount for the given metrics. */
export function estimateMountedCount(
  total: number,
  viewportHeight: number,
  estimateHeight: number,
  overscan: number,
  maxMounted: number,
): number {
  if (total <= 0) return 0;
  const visible = Math.ceil(viewportHeight / Math.max(1, estimateHeight)) + overscan * 2;
  return Math.min(total, Math.min(maxMounted, Math.max(1, visible)));
}
