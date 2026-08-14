#!/usr/bin/env python3
"""Fan-out existing `strike eval` over an instance list. Not a new runner."""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
import time
from concurrent.futures import ThreadPoolExecutor, as_completed
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
STRIKE = ROOT / "strike"
PIN_PATH = Path(__file__).resolve().parent / "PIN.json"


def load_ids(path: Path) -> list[str]:
    ids = []
    for line in path.read_text().splitlines():
        line = line.strip()
        if line and not line.startswith("#"):
            ids.append(line)
    if not ids:
        raise SystemExit(f"no ids in {path}")
    return ids


def instance_flags(ids: list[str]) -> list[str]:
    out: list[str] = []
    for i in ids:
        out.extend(["--instance", i])
    return out


def merge_swe(reports: list[dict], run_id: str, pin: dict) -> dict:
    results = []
    for r in reports:
        results.extend(r.get("results") or [])
    by_id = {row["instanceId"]: row for row in results}
    results = [by_id[k] for k in sorted(by_id)]
    attempted = resolved = unresolved = errors = skipped = 0
    tin = tout = 0
    cost = 0.0
    wall = 0
    graded = 0
    for row in results:
        tin += row.get("tokensIn") or 0
        tout += row.get("tokensOut") or 0
        cost += row.get("costUsd") or 0.0
        wall += row.get("wallClockMs") or 0
        st = row.get("status")
        if st == "resolved":
            resolved += 1
            attempted += 1
            graded += 1
        elif st == "unresolved":
            unresolved += 1
            attempted += 1
            graded += 1
        elif st == "error":
            errors += 1
            attempted += 1
        elif st == "skipped":
            skipped += 1
        else:
            attempted += 1
    pass_rate = (resolved / graded) if graded else 0.0
    return {
        "schemaVersion": "1.0.0",
        "benchmark": "swe-bench-verified-subset",
        "subsetSize": len(results),
        "runId": run_id,
        "generatedAt": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "provider": pin["provider"],
        "model": pin["model"],
        "effort": pin["effort"],
        "temperature": pin["temperature"],
        "reasoningEffort": pin.get("reasoning_effort_wire", "high"),
        "grader": "docker",
        "attempted": attempted,
        "resolved": resolved,
        "unresolved": unresolved,
        "errors": errors,
        "skipped": skipped,
        "passRate": pass_rate,
        "totalTokensIn": tin,
        "totalTokensOut": tout,
        "totalCostUsd": cost,
        "totalWallMs": wall,
        "note": "Internal regression signal only. Do not publish pass rates in product README (SWE-ABS caveat).",
        "results": results,
    }


def merge_tb(reports: list[dict], run_id: str, pin: dict) -> dict:
    results = []
    for r in reports:
        results.extend(r.get("results") or [])
    by_id = {row["instanceId"]: row for row in results}
    results = [by_id[k] for k in sorted(by_id)]
    attempted = resolved = unresolved = errors = skipped = 0
    tin = tout = 0
    cost = 0.0
    wall = 0
    graded = 0
    for row in results:
        tin += row.get("tokensIn") or 0
        tout += row.get("tokensOut") or 0
        cost += row.get("costUsd") or 0.0
        wall += row.get("wallClockMs") or 0
        st = row.get("status")
        if st == "resolved":
            resolved += 1
            attempted += 1
            graded += 1
        elif st == "unresolved":
            unresolved += 1
            attempted += 1
            graded += 1
        elif st == "error":
            errors += 1
            attempted += 1
        elif st == "skipped":
            skipped += 1
        else:
            attempted += 1
    pass_rate = (resolved / graded) if graded else 0.0
    return {
        "schemaVersion": "1.0.0",
        "benchmark": "terminal-bench-2-subset",
        "datasetPin": "terminal-bench-2@20251031",
        "subsetSize": len(results),
        "runId": run_id,
        "generatedAt": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "provider": pin["provider"],
        "model": pin["model"],
        "effort": pin["effort"],
        "temperature": pin["temperature"],
        "reasoningEffort": pin.get("reasoning_effort_wire", "high"),
        "grader": "docker",
        "attempted": attempted,
        "resolved": resolved,
        "unresolved": unresolved,
        "errors": errors,
        "skipped": skipped,
        "passRate": pass_rate,
        "totalTokensIn": tin,
        "totalTokensOut": tout,
        "totalCostUsd": cost,
        "totalWallMs": wall,
        "note": "Internal regression signal only. Do not publish pass rates in product README.",
        "results": results,
    }


def run_one(
    bench: str,
    instance: str,
    out_dir: Path,
    pin: dict,
    dataset: str,
    tasks_dir: str,
    strike_bin: str,
    extra: list[str],
) -> dict:
    inst_out = out_dir / "instances" / instance.replace("/", "_")
    inst_out.mkdir(parents=True, exist_ok=True)
    done = inst_out / "report.json"
    if done.exists():
        return json.loads(done.read_text())
    cmd = [
        strike_bin,
        "eval",
        bench,
        "--provider",
        pin["provider"],
        "--model",
        pin["model"],
        "--effort",
        pin["effort"],
        "--grader",
        "docker",
        "--instance",
        instance,
        "--out",
        str(inst_out),
        "--run-id",
        inst_out.name,
        "--strike-bin",
        strike_bin,
    ]
    if bench == "swebench":
        cmd.extend(["--dataset", dataset, "--agent-timeout", "30m"])
    else:
        cmd.extend(["--tasks-dir", tasks_dir])
    cmd.extend(extra)
    log = inst_out / "runner.log"
    t0 = time.time()
    with log.open("w") as lf:
        lf.write(" ".join(cmd) + "\n")
        lf.flush()
        proc = subprocess.run(cmd, stdout=lf, stderr=subprocess.STDOUT, cwd=str(ROOT))
    elapsed = time.time() - t0
    if not done.exists():
        raise RuntimeError(f"{instance}: strike eval exit {proc.returncode} after {elapsed:.0f}s; see {log}")
    rep = json.loads(done.read_text())
    # Keep images across instances/repeats so Docker Hub rate limits are not
    # burned by re-pulls. Prune only when the root filesystem is tight.
    maybe_prune_images()
    return rep


def maybe_prune_images() -> None:
    try:
        import shutil

        free = shutil.disk_usage("/").free
        if free > 80 * 1024**3:
            return
        subprocess.run(["docker", "image", "prune", "-f"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    except Exception:
        pass


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--bench", choices=["swebench", "tbench"], required=True)
    ap.add_argument("--ids", type=Path, required=True)
    ap.add_argument("--out", type=Path, required=True)
    ap.add_argument("--run-id", required=True)
    ap.add_argument("--jobs", type=int, default=4)
    ap.add_argument("--dataset", default=os.path.expanduser("~/.strike/eval/datasets/swebench_verified.jsonl"))
    ap.add_argument("--tasks-dir", default=os.path.expanduser("~/.strike/eval/tbench/terminal-bench-2"))
    ap.add_argument("--strike-bin", default=str(STRIKE))
    args, extra = ap.parse_known_args()

    pin = json.loads(PIN_PATH.read_text())
    ids = load_ids(args.ids)
    args.out.mkdir(parents=True, exist_ok=True)
    (args.out / "PIN.json").write_text(json.dumps(pin, indent=2) + "\n")

    reports: list[dict] = []
    errors: list[str] = []
    print(f"run {args.run_id} bench={args.bench} n={len(ids)} jobs={args.jobs}", flush=True)
    with ThreadPoolExecutor(max_workers=max(1, args.jobs)) as ex:
        futs = {
            ex.submit(
                run_one,
                args.bench,
                iid,
                args.out,
                pin,
                args.dataset,
                args.tasks_dir,
                args.strike_bin,
                extra,
            ): iid
            for iid in ids
        }
        done_n = 0
        for fut in as_completed(futs):
            iid = futs[fut]
            done_n += 1
            try:
                rep = fut.result()
                reports.append(rep)
                rows = rep.get("results") or []
                st = rows[0].get("status") if rows else "?"
                print(f"[{done_n}/{len(ids)}] {iid} {st}", flush=True)
            except Exception as e:
                errors.append(f"{iid}: {e}")
                print(f"[{done_n}/{len(ids)}] {iid} ERROR {e}", flush=True)

    merge = merge_swe if args.bench == "swebench" else merge_tb
    summary = merge(reports, args.run_id, pin)
    summary["shardErrors"] = errors
    out_path = args.out / "report.json"
    out_path.write_text(json.dumps(summary, indent=2) + "\n")
    print(
        f"wrote {out_path} resolved={summary['resolved']}/{summary['attempted']} "
        f"passRate={summary['passRate']:.3f} cost=${summary['totalCostUsd']:.2f} errors={len(errors)}",
        flush=True,
    )
    return 1 if errors else 0


if __name__ == "__main__":
    sys.exit(main())
