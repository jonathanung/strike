#!/usr/bin/env python3
"""Sum API $ from evals/loop/results/*/report.json."""

from __future__ import annotations

import json
from pathlib import Path

ROOT = Path(__file__).resolve().parent / "results"
STOP = 3500.0


def main() -> None:
    total = 0.0
    rows = []
    seen = set()
    if ROOT.exists():
        paths = list(ROOT.glob("*/report.json")) + list(ROOT.glob("*/instances/*/report.json"))
        # Prefer merged reports; if missing, sum instance shards (avoid double-count).
        merged_dirs = {p.parent for p in ROOT.glob("*/report.json")}
        for p in sorted(paths):
            try:
                r = json.loads(p.read_text())
            except Exception:
                continue
            if p.parent.name == "instances":
                continue
            parent = p.parent
            if parent.parent.name == "instances":
                if parent.parent.parent in merged_dirs:
                    continue
                key = (parent.parent.parent.name, (r.get("results") or [{}])[0].get("instanceId"))
                if key in seen:
                    continue
                seen.add(key)
            c = float(r.get("totalCostUsd") or 0)
            total += c
            rows.append((c, r.get("runId") or parent.name, r.get("resolved"), r.get("attempted"), r.get("passRate")))
    print(f"cumulative_cost_usd={total:.2f} stop={STOP:.0f} remaining={STOP-total:.2f}")
    for c, rid, res, att, pr in rows:
        print(f"  ${c:8.2f}  {rid}  {res}/{att}  pass={pr}")


if __name__ == "__main__":
    main()
