---
description: scaffold project container config and Dockerfile.devcontainer via questions
---
Scaffold a strike-native dev container for this repo: detect deps, ask the human,
write layered container config, preview the Dockerfile, then eject only after confirm.

$ARGUMENTS

## Hard rules

1. **Never** write or overwrite `Dockerfile.devcontainer` (or any Dockerfile) without
   showing the full proposed diff/content and getting an explicit yes via the
   `question` tool (or a clear user message that already approved that exact content).
2. **Always** use the `question` tool for: base image, dependency set, network posture,
   and resource limits — even when detection is confident. Defaults may be pre-selected
   but the human must confirm.
3. Prefer layered config (`.strike/container.json` / project `container` block) +
   `strike container eject` over hand-editing Dockerfiles.
4. Do not bake credentials into images. Auth stays on `container.auth.forwardEnv` /
   env files at launch time.
5. Do not run `docker build` / launch unless the user explicitly asks after eject.

## Context (run first)

Via the bash tool from the project root:

1. `git rev-parse --show-toplevel 2>/dev/null || pwd`
2. `strike container detect` (JSON summary of markers + suggested config)
3. `test -f Dockerfile.devcontainer && strike container drift || true`
4. `test -f .strike/container.json && cat .strike/container.json || true`
5. `test -f .strike/container.jsonc && cat .strike/container.jsonc || true`
6. Skim root manifests the detector reported (`go.mod`, `package.json`, …)

If `strike container detect` is unavailable, fall back to reading those manifests
manually and note that the binary is stale.

## Task

### 1. Summarize detection

Present a short bullet list: languages found, suggested apt packages, existing
Dockerfile/config/drift state. Do not write files yet.

### 2. Ask (required `question` calls)

Ask with the `question` tool (batch related options when the tool allows multiple
questions; otherwise sequential). Cover at least:

| Topic | Options / guidance |
|---|---|
| **Base image** | `ubuntu:24.04` (default), `ubuntu:22.04`, `debian:bookworm`, or custom tag the user types |
| **Dependencies** | Accept detected set / edit list (node, python, go toolchains, extra apt packages). For Nix-only repos, warn that full Nix-in-Docker is out of scope — offer base image + manual packages instead |
| **Network posture** | `default` (bridge) vs `none` (offline). Prefer `default` unless the user wants air-gapped |
| **Resources** | memory (e.g. empty/unlimited, `2g`), cpus (empty or `2`), pidsLimit (default `512`) |

Optional follow-ups when relevant: publish ports, `execution: container` vs keep
`local` (eject-only), forward SSH agent (Linux).

If the user already answered these in `$ARGUMENTS` with explicit values, still
confirm once with `question` summarizing the interpreted choices (single
yes/edit/cancel).

### 3. Emit config (not Dockerfile yet)

Write or update **project** container config only after step 2:

- Prefer `.strike/container.json` (or `.jsonc`) with the confirmed fields:
  `baseImage`, `packages`, `needsNode` / `nodeVersion`, `needsPython` /
  `pythonVersion`, `needsGo` / `goVersion`, `needsRust`, `network.mode`,
  `resources`, optional `execution`.
- Merge with any existing file; do not drop unrelated keys.
- Show the config diff to the user.

### 4. Preview Dockerfile, then eject

1. Render a preview without writing when possible:
   - `strike container eject --out /tmp/strike-devcontainer-preview.Dockerfile` then
     read that file and **delete** it, **or**
   - explain you will run eject to the real path only after confirm.
2. Show the full proposed `Dockerfile.devcontainer` body (or a unified diff against
   the existing file).
3. `question`: **Write Dockerfile.devcontainer?** → `yes` / `yes --force` (if drift) /
   `cancel`.
4. On yes: `strike container eject` (add `--force` only when the user chose force or
   drift was explained and approved).
5. On cancel: leave config as written; do not touch the Dockerfile.

### 5. Finish

Report:

- Config path written
- Dockerfile path + config hash (from eject output)
- Next steps: `strike --launch-inside-container` or set `"execution": "container"`
- Remind: commit `Dockerfile.devcontainer` if the team shares the image recipe

## Safety

- Never commit secrets or put API keys in Dockerfile/`container` JSON.
- Never `--force` eject without explaining drift (have vs want hash).
- Never enable `network.mode: none` without the user picking it.
- If eject fails, print the error verbatim and stop.
