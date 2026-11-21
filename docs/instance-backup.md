# Instance backup and restore

One portable file for the whole gateway (#1339). Settings → **Backup** in the
web cockpit, or the daemon routes directly:

| Route | What it does |
|---|---|
| `GET /v1/backup/export` | Downloads `strike-backup.json`. |
| `POST /v1/backup/restore?dryRun=1` | Checks the archive and reports what a restore would do. Writes nothing. Allowed in attach-only mode. |
| `POST /v1/backup/restore` | Restores. Needs a live gateway. |

## What is in the archive (version 1)

| Section | Contents | Restored through |
|---|---|---|
| `settings` | default provider, model, agent, reasoning effort, theme | `host.Settings` |
| `memory` | project and global memory entries (key, value, tags, author) | `host.Memory` / `MemoryScopes` |
| `knowledge` | project and global knowledge pins (path, note, author) | `host.Knowledge` |
| `schedules` | prompt, goal and workflow schedule definitions (no run history) | `schedule.Service.Create` |
| `sessions` | every session as a redacted session package (same format as single-session export), with parent/lead links | `session.Manager` |

Restore never drops files into the data directory. Each record goes through
its store's own write path.

## What is never in the archive

The manifest's `excluded` list names every category left out, so the gap can
be checked:

- **Secrets:** provider credentials (`~/.strike/auth.json`), gateway bearer
  tokens and pairing codes, secret references and environment values.
  Credential-shaped text inside sessions, memory, knowledge pin notes and
  schedule prompts is scrubbed by the same redaction as session export.
- **Trust settings:** permission mode and sandbox settings, global and per
  schedule. Script schedules (commands can embed credentials, and they must
  be re-armed on the target).
- **Not covered yet:** artifacts, attachments, goals and
  autopilot state, workspaces and their rules, issues, plans, ledger.

The file is plain JSON and **not encrypted** (`"encrypted": false`). Treat it
like the session logs it contains.

## Integrity

Each section's sha256 (over its compact JSON) is in the manifest. A restore
checks every hash, the item counts, and that no section exists outside the
manifest before it plans anything. A failed check refuses the whole archive.
The hashes catch truncation and corruption. They are **not a signature**:
anyone who edits a section can recompute its hash. For that reason a restore
never applies trust settings from the file, and restored schedules start
paused.

## Conflict policy

Nothing on the target is overwritten silently:

| Situation | Action |
|---|---|
| Record not on the target | `create` (sessions keep their original id) |
| Same id/key and identical content | `skip` |
| Same id/key, different content | `copy`: a session gets a new id and a `(restored)` title; a memory entry or schedule gets a `(restored)` name |
| Setting already set to a different value | `skip`, keeping the target's value |
| Knowledge pin already on the target | `skip`. The path is the pin's identity, so a different note is kept, never copied. |
| Target refuses the record (missing store, invalid schedule) | `fail`. The other records still restore, and the report lists every decision. |

Restoring the same archive twice is a no-op the second time.

## Format

```json
{
  "format": "strike.instance-backup",
  "version": 1,
  "createdAt": "2026-11-20T12:00:00Z",
  "strikeVersion": "v0.x",
  "encrypted": false,
  "manifest": {
    "sections": [{"name": "memory", "items": 2, "bytes": 120, "sha256": "…"}],
    "excluded": [{"category": "provider credentials …", "kind": "secret", "reason": "…"}]
  },
  "sections": {"memory": [ … ]}
}
```

Knowledge pins store paths as written. A project-relative path points into
whatever project the target opens; an absolute path is restored as is and
may not exist on another machine. A gateway older than the `knowledge`
section refuses such an archive with `unknown section "knowledge"`.

Exclusion `kind` is `secret`, `policy`, `not-yet-included`, or `unavailable`
(a store this gateway does not run). The code lives in
`internal/persist/backup` (format) and
`internal/frontend/server/instance_backup.go` (routes).
