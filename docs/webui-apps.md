# Web UI apps

An app is a queue the daemon can fill. The page in `webui/src/apps.rs` draws
a launcher, a list, a reading pane, triage buttons, and **Hand off to an
agent**. It does not embed a product, and it does not invent rows. Cards
come from `GET /v1/apps`.

GitHub triage (`github-triage`) is the only registered app. It shells out
to `gh` on the daemon host, in the directory `strike serve` was started in
(`Options.WorkDir`). The browser never sends a GitHub token and never
chooses the repository. The repo is `origin` when that remote is
github.com, otherwise the first other github.com remote. The daemon
reads the URL stored in the repo (`git config`), not `git remote get-url`,
and drops any `user:password@` before it builds `owner/name`.

## HTTP

All routes require the daemon bearer, same as the rest of `/v1/`.

| Method | Path | Body |
|---|---|---|
| GET | `/v1/apps` | `{ "apps": [{ "id", "title", "summary", "handoff" }] }` |
| GET | `/v1/apps/{id}/items?kind=issue\|pr\|all&state=open\|closed` | `{ "repo", "items": [ item ] }` |
| GET | `/v1/apps/{id}/items/{itemID}` | item plus `body`, `comments`, `actions` |
| POST | `/v1/apps/{id}/items/{itemID}/act` | `{ "action", "body?" }` → `{ "ok": true }` |
| POST | `/v1/apps/{id}/items/{itemID}/handoff` | `{ "ok", "rootId", "sessionId" }` |

Item:

```json
{
  "id": "issue-1283",
  "title": "",
  "subtitle": "author",
  "badge": "open",
  "updatedAt": "2026-10-26T06:47:45Z",
  "labels": [],
  "url": "https://github.com/owner/repo/issues/1283",
  "kind": "issue",
  "number": 1283
}
```

`actions` is `{ "id", "label", "needsBody" }`. The client posts `id` back.
Handoff is not an action: when `handoff` is true the client calls the
handoff route. That route creates a root session and submits `user.input`
whose text is the live item (title, body, url). The prompt is redacted
with `pkg/redact` before it is queued.

Errors are `{ "error": "..." }` and no `items` array. A failure must not
be rendered as an empty successful queue of made-up work. GitHub failures
are HTTP 502. A bad id or comment is HTTP 400. An unknown app is HTTP 404.
Read-only serve rejects act and handoff with HTTP 403. No live engine
rejects handoff with HTTP 503.

## Adding an app

1. Implement the queue methods next to `githubTriageApp` in
   `internal/frontend/server/apps_github.go` (or a new `apps_<name>.go`).
2. Append it from `queueApps` in `internal/frontend/server/apps.go`.
3. Use a single-path-segment id (`inbox`, `schedule-results`). Item ids
   must be one segment, `[A-Za-z0-9._-]`, so they fit `{itemID}`.
4. Resolve data on the daemon. Do not accept a repo, token, or filesystem
   path from the browser.
5. Return real rows only. If the store does not exist yet, do not register
   the app. Say so in `docs/webui-planned.md` instead of shipping a blank card.
6. Cover the route with `httptest` and a fake of the external command.
   Do not call the network.

The client does not need a branch per app. It already lists whatever
`GET /v1/apps` returns and posts the actions the detail lists.

Inbox and schedule results are intentionally not registered. There is no
inbox store, and there is no scheduler in `strike serve`.
