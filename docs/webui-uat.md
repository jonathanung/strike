# Web UI UAT

How a PR author captures the running cockpit. The harness boots
`strike serve --provider echo`, drives headless Chrome, writes named
screenshots, and (unless `--no-video`) a screen recording of one scripted
pass. It exits non-zero when an assertion fails.

No product UI lives here. Kiro Crew frames are a reference you pass in; the
harness does not vendor them.

## One command

From a checkout that already has `./strike` (or set `STRIKE_BIN`):

```sh
make webui-uat
```

That builds the binary, runs `node --test scripts/webui-uat/lib.test.mjs`,
then:

```sh
node scripts/webui-uat.mjs
```

Artifacts (gitignored) land in `webui-uat-artifacts/`:

| Path | What |
|---|---|
| `screenshots/<name>.png` | PNG at a fixed CSS viewport, device pixel ratio 1 |
| `dom/<name>.json` | Tokens plus the visible text at that shot |
| `frames/` | JPEG frames used to encode the video |
| `uat.mp4` | The scripted pass at 1440×900 |
| `report.json` | Shot list and assertion failures |

Chrome is `google-chrome` or `$CHROME_BIN`. ffmpeg is required for the video
and for side-by-side stills. `--no-video` skips ffmpeg.

## What the pass does

1. Waits until the shell shows `Run a command` and `Activity`, and the dark
   Strike tokens are `--acid: #7c3aed` and `--ground: #14131c`
   (`scripts/webui-uat/fixtures/shell-dark.json`).
2. Keyboard: Ctrl+K, type `plan`, Enter. The composer chip becomes
   `Approval · Plan`.
3. Opens the approval menu (Plan, Soft, Edits, Yolo) and Escape.
4. Opens Files and Changes.
5. Types `uat-marker-alpha` and Send. The transcript must show that marker
   and the echo provider's `You said:`.
6. `POST /v1/ops` `team.spawn` with objective `Summarize the working directory`
   and name `uat-child`. The Activity row must reach `blocked` (echo children
   stop on `schema:handoff`).
7. New session, click back to the previous row, Alt+Left.
8. Daemon view, then Chat.
9. Scheme Light (ground `#ffffff`, acid `#5b21b6`), then Scheme Dark again.
10. Stops the daemon. The page must show Unreachable or
    `Could not reach the daemon`, with Retry. Starts the same binary on the
    same port and clicks Retry. The chip must read Connected.

Named shots are taken at 1024×594 and 1440×900. The empty shell, the
composer, and the reconnect error also get a 390×844 shot where the layout
breaks to one column (`shell-empty-mobile.png`, `shell-composer-mobile.png`).

## CI

`.github/workflows/ci.yml` job `webui-smoke` keeps the auth check in
`scripts/webui-smoke.mjs`, then runs:

```sh
node --test scripts/webui-uat/lib.test.mjs
node scripts/webui-uat.mjs --suite smoke --no-video --out webui-uat-artifacts
```

`--suite smoke` is the same browser pass. Video stays off in CI so the job
does not need ffmpeg. Screenshots upload as the `webui-uat` artifact.

## Side-by-side with a Kiro frame

Pass two PNGs you already have. The left image is the reference, the right
is a Strike shot. Both are Lanczos-scaled to one height (default 594, the
review CSS height) and placed side by side. Nothing is copied into git.

```sh
node scripts/webui-uat.mjs --compare-only \
  --left /path/to/kiro-frame.png \
  --right webui-uat-artifacts/screenshots/shell-empty-1024.png \
  --compare-out webui-uat-artifacts/side-by-side/empty.png
```

`--height 594` overrides the shared height. Use this for design review.
Do not commit Kiro branding or assets.

## Coarse goldens

Full-frame pixel diffs move when the empty-state headline rotates or a
session time ticks, and Chrome's font hinting differs between machines.
The committed check is the DOM/token fixture, not a bitmap.

`--goldens` also downscales a shot to 32×18 and compares the mean absolute
channel delta to `scripts/webui-uat/goldens/<name>.rgb`.
`--update-goldens` writes those files. The default gate does not require
them. A missing golden fails only when `--goldens` is set.

## Flags

| Flag | Effect |
|---|---|
| `--out dir` | Artifact directory (default `webui-uat-artifacts`) |
| `--suite full\|smoke` | Recorded in `report.json`. Smoke is what CI runs |
| `--no-video` / `--video` | Skip or force `uat.mp4` |
| `--goldens` / `--update-goldens` | Opt-in downscale compare |
| `--compare-only --left --right --compare-out` | Side-by-side, no daemon |
| `--height N` | Side-by-side height in pixels |

`STRIKE_BIN` selects the daemon binary. The process is
`strike serve --addr 127.0.0.1:<port> --provider echo` with a temporary
`HOME`. Loopback, no `--auth`, matching the local path in `docs/gateway.md`.
The auth rejection check stays in `scripts/webui-smoke.mjs`.
