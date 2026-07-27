---
name: release
description: Cut a strike-cli GitHub release from main — preflight, notes, annotated tag, watch release.yml, verify assets. Use when shipping a version (v0.x.y), tagging, or preparing release notes.
---

# Release (strike-cli)

Ship a version tag that triggers `.github/workflows/release.yml` (multi-arch archives + GitHub Release).

## Preconditions

- `gh auth status` ok; push rights to the repo that owns tags.
- Working tree clean on an up-to-date `main` (or explicit commit SHA to tag).
- User confirmed version number (e.g. `v0.1.0`) and that this is intentional.

Stop-and-ask if main CI is red, an open release-block issue exists, or the tag already exists.

## Preflight

```sh
git fetch origin main --tags
git checkout main
git merge --ff-only origin/main
git status -sb   # must be clean, main == origin/main
gh run list --branch main --limit 3   # latest CI success preferred
git tag -l 'v*' | tail -5
git log "$(git describe --tags --abbrev=0)..HEAD" --oneline
```

Load `test-and-validate` tier B (or C if trust-boundary commits dominate since last tag). Prefer green CI on `main` over re-running the full suite locally when CI is already success on HEAD.

Optional: load `smoke` for a quick product check.

## Notes

Draft notes from `git log PREV..HEAD` (feat/fix highlights). Do **not** ship install-only boilerplate as the entire body.

Include:

- Install snippet (`curl …/install` or project standard)
- Highlights since previous tag (bullets)
- Breaking/default changes (keybinds, config) if any
- Upgrade note when relevant

## Tag and push

```sh
VERSION=v0.x.y   # user-confirmed
git tag -a "$VERSION" -m "strike $VERSION

<one-line summary>
"
git push origin "$VERSION"
```

Never force-push tags. Never tag a dirty tree or a commit not on `origin/main` unless the user explicitly requests a pre-release from a branch.

## Watch workflow

```sh
gh run list --workflow=release.yml --limit 3
gh run watch <id> --exit-status
gh release view "$VERSION"
```

Confirm assets: linux/darwin × amd64/arm64 tarballs + `checksums.txt`.

## Enrich GitHub release body

`release.yml` may create minimal notes — edit immediately:

```sh
gh release edit "$VERSION" --notes-file notes.md
```

## Post-verify

```sh
gh release view "$VERSION" --json tagName,assets,url
# optional: strike --upgrade / install script against the new tag
```

## Report

- Tag, commit SHA, release URL
- Workflow conclusion
- Asset names
- Whether notes were enriched

## Hard rules

1. No force tag move; no secrets in notes.
2. No release while main CI is failing unless user overrides in writing.
3. Annotated tags only (`-a`).
4. Prefer patch/minor policy the user stated; default to their explicit version string.
