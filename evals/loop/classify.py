#!/usr/bin/env python3
"""Classify DEV failures from strike session JSONL + eval report."""

from __future__ import annotations

import json
import os
import re
import sys
from pathlib import Path

BUCKETS = (
    "HARNESS-TOOL",
    "HARNESS-CONTEXT",
    "HARNESS-LOOP",
    "HARNESS-OUTPUT",
    "PROMPT",
    "CAPABILITY",
    "INFRA",
)

SESS_DIR = Path.home() / ".strike" / "sessions"


def session_path(sid: str) -> Path:
    return SESS_DIR / f"{sid}.jsonl"


def load_jsonl(path: Path) -> list[dict]:
    rows = []
    if not path.exists():
        return rows
    for line in path.read_text(errors="replace").splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            rows.append(json.loads(line))
        except json.JSONDecodeError:
            continue
    return rows


def classify_row(row: dict) -> tuple[str, str]:
    err = (row.get("error") or "") + " " + (row.get("gradeDetail") or "")
    err_l = err.lower()
    status = row.get("status")
    sid = row.get("sessionId") or ""
    events = load_jsonl(session_path(sid)) if sid else []

    if status == "error" or any(
        k in err_l
        for k in (
            "rate limit",
            "429",
            "api error",
            "oauth",
            "unauthorized",
            "docker",
            "materialize",
            "pull ",
            "no such image",
            "network",
            "connection reset",
            "timeout waiting",
        )
    ):
        if "docker" in err_l or "materialize" in err_l or "image" in err_l:
            return "INFRA", "docker/materialize"
        if any(k in err_l for k in ("rate", "429", "api", "oauth", "unauthorized", "token")):
            return "INFRA", "api/auth"
        if status == "error":
            return "INFRA", err[:160]

    texts = []
    tool_names = []
    tool_errors = []
    stop_reasons = []
    for ev in events:
        t = ev.get("type") or ev.get("Type") or ""
        payload = ev.get("data") if isinstance(ev.get("data"), dict) else ev
        if t == "tool.begin":
            name = payload.get("name") or ""
            if name:
                tool_names.append(name)
        if t in ("tool.end", "tool.output"):
            name = payload.get("name") or ""
            out = str(payload.get("output") or payload.get("data") or payload.get("result") or "")
            if payload.get("isError") or payload.get("error") or "error:" in out.lower()[:80]:
                tool_errors.append((name, out[:300]))
            texts.append(out[:500])
        if t in ("turn.completed", "turn.end", "error"):
            stop_reasons.append(str(payload.get("stopReason") or payload.get("error") or ""))
        if t in ("text.delta", "message"):
            texts.append(str(payload.get("text") or payload.get("delta") or "")[:400])

    blob = "\n".join(texts).lower()
    tools = " ".join(tool_names).lower()
    terr = " ".join(e for _, e in tool_errors).lower()

    if "network_denied" in terr or "network_denied" in blob or "network_denied" in err_l:
        return "HARNESS-TOOL", "network.allow blocked required command"
    if any(
        s in terr or s in blob
        for s in (
            "file not found",
            "no such file",
            "patch apply",
            "apply_patch",
            "failed to apply",
            "malformed",
            "invalid patch",
            "hunk failed",
            "does not exist",
        )
    ):
        return "HARNESS-TOOL", "edit/patch/path failure"
    if "empty patch" in err_l or (status == "unresolved" and (row.get("patchBytes") or 0) == 0):
        return "HARNESS-OUTPUT", "empty or missing patch"
    if any(s in blob or s in err_l for s in ("context window", "truncated", "too long", "max tokens", "prompt too")):
        return "HARNESS-CONTEXT", "context/truncation"
    if any(s in blob or s in err_l for s in ("turn limit", "max turns", "timed out", "deadline", "agent timeout")):
        return "HARNESS-LOOP", "timeout/turn limit"
    # retry loops: many identical tool errors
    if len(tool_errors) >= 6:
        return "HARNESS-LOOP", f"{len(tool_errors)} tool errors"
    if re.search(r"edit(ed)? the test|modified tests|changed the test", blob):
        return "PROMPT", "edited tests"
    if re.search(r"skip(ped)? (the )?test|did not run test|no test", blob) and status == "unresolved":
        return "PROMPT", "skipped tests / stopped early"

    if status in ("unresolved", "error"):
        # default: capability unless harness-shaped
        if "empty patch" in err_l:
            return "HARNESS-OUTPUT", "empty patch"
        return "CAPABILITY", "unresolved after agent run"
    return "CAPABILITY", "fallback"


def classify_report(report_path: Path) -> dict:
    rep = json.loads(report_path.read_text())
    counts = {b: 0 for b in BUCKETS}
    rows_out = []
    for row in rep.get("results") or []:
        if row.get("status") == "resolved":
            continue
        bucket, why = classify_row(row)
        counts[bucket] += 1
        rows_out.append(
            {
                "instanceId": row.get("instanceId"),
                "status": row.get("status"),
                "sessionId": row.get("sessionId"),
                "bucket": bucket,
                "why": why,
                "patchBytes": row.get("patchBytes"),
                "error": (row.get("error") or "")[:240],
            }
        )
    nfail = sum(counts.values())
    return {
        "report": str(report_path),
        "runId": rep.get("runId"),
        "model": rep.get("model"),
        "effort": rep.get("effort"),
        "temperature": rep.get("temperature"),
        "resolved": rep.get("resolved"),
        "attempted": rep.get("attempted"),
        "passRate": rep.get("passRate"),
        "failures": nfail,
        "counts": counts,
        "capabilityShare": (counts["CAPABILITY"] / nfail) if nfail else 0.0,
        "rows": rows_out,
    }


def main() -> int:
    if len(sys.argv) < 2:
        print("usage: classify.py <report.json> [report.json...]", file=sys.stderr)
        return 2
    for p in sys.argv[1:]:
        out = classify_report(Path(p))
        dest = Path(p).with_name("taxonomy.json")
        dest.write_text(json.dumps(out, indent=2) + "\n")
        print(f"wrote {dest} failures={out['failures']} counts={out['counts']} cap={out['capabilityShare']:.2f}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
