#!/usr/bin/env python3
"""Mean/stddev resolve rate for the three Phase 2 DEV repeats."""

from __future__ import annotations

import json
import math
import statistics
from pathlib import Path

ROOT = Path(__file__).resolve().parent / "results"


def load(prefix: str) -> list[dict]:
    out = []
    for n in (1, 2, 3):
        p = ROOT / f"{prefix}-{n}" / "report.json"
        if p.exists():
            out.append(json.loads(p.read_text()))
    return out


def stats(reps: list[dict]) -> dict:
    rates = [float(r.get("passRate") or 0) * 100 for r in reps]
    costs = [float(r.get("totalCostUsd") or 0) for r in reps]
    if not rates:
        return {"n": 0}
    mean = statistics.fmean(rates)
    sd = statistics.stdev(rates) if len(rates) > 1 else 0.0
    return {
        "n": len(rates),
        "rates_pct": rates,
        "mean_pct": mean,
        "stdev_pct": sd,
        "significance_floor_pct": 2 * sd,
        "costs": costs,
        "mean_cost": statistics.fmean(costs) if costs else 0,
    }


def main() -> None:
    swe = stats(load("baseline-swe-dev"))
    tb = stats(load("baseline-tb-dev"))
    summary = {
        "model": "grok-4.6",
        "effort": "high",
        "temperature": "unset",
        "swe_dev": swe,
        "tb_dev": tb,
    }
    dest = ROOT / "baseline-summary.json"
    dest.parent.mkdir(parents=True, exist_ok=True)
    dest.write_text(json.dumps(summary, indent=2) + "\n")
    print(json.dumps(summary, indent=2))


if __name__ == "__main__":
    main()
