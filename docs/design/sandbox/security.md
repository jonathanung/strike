# Sandbox security: NixOS microVM for agents

Status: feasibility note for
[#1290](https://github.com/jonathanung/strike/issues/1290). Docs only.
Sibling angles (architecture, runtime, self-config, product) own placement,
hypervisors, and UI. This page owns the threat model.

Related product surfaces:
[#1266](https://github.com/jonathanung/strike/issues/1266) approvals,
[#1286](https://github.com/jonathanung/strike/issues/1286) audit,
[#1279](https://github.com/jonathanung/strike/issues/1279) terminal,
[#1260](https://github.com/jonathanung/strike/issues/1260) workspace roots.
Current behavior: [isolation.md](../../isolation.md),
[threat-model.md](../../threat-model.md), [secrets.md](../../secrets.md),
[audit.md](../../audit.md), [gateway.md](../../gateway.md),
[auth.md](../../auth.md).

## Verdict

**Go with conditions.**

A microVM is a real isolation boundary for an agent that has a shell: the
guest kernel is not the host kernel, so a container escape class goes away.
It does not make prompt injection harmless, it does not replace permission
asks, and it does not help if the host mounts secrets into the guest "so
git and the model work."

Conditions, all required before a VM session is a supported mode:

1. The engine, `~/.strike/auth.json`, and the gateway token stay on the
   host. Provider calls are made by the host process.
2. The guest mount set is an explicit allow-list of workspace roots. No
   home directory, no `~/.strike`, no SSH agent, no nix-daemon socket, no
   host container socket.
3. Default guest network is off. A later HTTP(S) proxy may enforce
   `network.allow`. `yolo` and `--dangerously-skip-permissions` must not
   widen mounts or that allow-list.
4. A negative probe from inside the guest fails the boot if a forbidden
   path or a host service is reachable. Copying a firewall table without
   that probe is not done.
5. Agent commands stay on the mediated tool path (permission, audit,
   content guard). A raw PTY is a separate, user-opened bypass.

## Effort

| Slice | Size | What it is |
|---|---|---|
| This note | **S** | Threat model only |
| MVP below | **L** | Host engine, mount policy, egress off, mediated exec, probe, audit events |
| Credential-injecting proxy, still host-issued | **L** added on the MVP | CONNECT allow-list, placeholder injection, response scrub. No new IdP or vault |
| nix-openclaw control plane (Keycloak, OpenBao, OPA, Temporal, in-guest engine) | **XL** | Second trust plane. Not the recommendation |

Sizes count new trust-boundary machinery, not calendar time. **S** is a
doc or a check inside an existing package. **M** is one host service that
reuses current enforcement. **L** is a new process boundary plus the probes
that make the boundary honest. **XL** is a new identity, secret store, and
policy product.

## Evidence

Read at these commits. Nothing here was booted.

| Tree | Commit | What was read |
|---|---|---|
| [dayvidpham/nix-openclaw-vm](https://github.com/dayvidpham/nix-openclaw-vm) | `909b8a6cbf4ad50c12ff9773413a6161168cce45` (2026-05-17) | `ARCHITECTURE.md`, `credential-proxy/model/README.md`, `credential-proxy/proxy/{placeholder,sanitizer,handlers}.go`, `credential-proxy/authz/policies/credential_access.rego`, `credential-proxy/config/config.go`, `modules/credential-proxy/guest.nix`, `modules/openclaw-vm/{default,guest}.nix` (lockdown nftables, virtiofs store share), `docs/research/{secretless-broker,openbao,keycloak,temporal}.Rmd` headings and the proxy pipeline they describe |
| [peasant-labs/infra](https://github.com/peasant-labs/infra) | `6f3b987af7026aef195c29f1e6627297068aef42` (2026-10-03) | `docs/runner-vm-spike-report.md`, `.github/workflows/runner-vm-security-probe.yml`, `runner-vm-network-diag.yml`, `runner-image.yml` |
| strike `main` | working tree at authoring time | permission mode (`harness/permission`), `pkg/redact`, `harness/providers/auth`, `harness/tool/bash_env.go`, `harness/tool/webfetch.go` (`isBlockedIP`), `internal/integrate/mcp`, `internal/trust/audit`, `docs/gateway.md` |

Not run, and why:

- No NixOS microVM, Cloud Hypervisor, or QEMU guest. `/dev/kvm` is present
  and `vmx` is in `/proc/cpuinfo`, but `open("/dev/kvm")` returned
  `EACCES`. `qemu-system-x86_64`, `cloud-hypervisor`, and `nix` were not
  installed. Nested boot was not attempted.
- TLC was not run. The model's own README records a passing check (154
  states, 89 distinct, depth 11) for concurrency invariants only.
- `credential-proxy` `go test` was not run.
- Peasant workflows are `workflow_dispatch` on a self-hosted `microvm`
  runner. They were read, not executed. The 2026-09-30 spike report still
  lists the negative probe as **not exercised**; the workflow file exists
  on that later commit and is the probe the report asked for.

## Threat model

### Adversary

The model will follow text from the workspace, from MCP, and from the web
([threat-model.md](../../threat-model.md)). Treat the guest as hostile
after the first tool call: the agent has a shell, can write the mounted
tree, and will try to read whatever is mounted and to open whatever the
network allows. The user who started `strike serve` is trusted. The host
kernel, the hypervisor, and virtiofsd are the trusted computing base, not
a proof.

A second user on the same host, and a second Strike session, are
neighbors. They are not the same principal as this guest.

### What the VM is for

| Asset | Today (host + bwrap) | With a microVM, if the conditions hold |
|---|---|---|
| Provider keys, OAuth refresh, gateway token | Host process and `0600` files. Bash does not inherit the process env (`BashMinimalEnvKeys`) | Same files, and they are not mounted. A guest root shell still cannot open them |
| Host kernel integrity | Shared with every container and with bwrap | Guest exploit stops at KVM, unless a device bug crosses it |
| Workspace the user pointed at | The agent is supposed to edit it | Still shared, on purpose, via one mount |
| Other paths on the host (`~`, other repos, `/run/credentials`) | bwrap hides some; `sandbox: off` hides none | Absent unless listed in the mount set |
| Network | `network.allow` is an application check. Bash preflight catches known clients only. OS sandbox net is on/off, not per host ([isolation.md](../../isolation.md)) | Default off. Optional proxy is the only path, and it is outside the guest |

### What a VM does not fix

| Attack | Why the VM is the wrong layer |
|---|---|
| Prompt injection in a file, MCP result, or fetched page | The model still runs the next tool. The VM bounds the tool's reach |
| Secrets the user put in the workspace (`.env`, PEM files, `.git-credentials`) | Those bytes are the mount. Content guard covers host `write`/`edit`; a guest shell writes around it unless exec is mediated |
| `yolo` skipping asks | Asks and isolation are different dials today, and they stay different |
| A stolen gateway token | [gateway.md](../../gateway.md): anyone who can reach the bind address and knows the token is the user. The VM does not shrink that |
| Host root, a malicious hypervisor, a virtiofsd bug | Below the boundary |

### Reference designs, and what Strike should take

**nix-openclaw-vm** puts the agent in a microVM and a MITM proxy on the
host. The invariant worth keeping is quoted from its architecture note:
the agent can use a credential and still not see it. Mechanisms that
implement that invariant:

| Piece | Where | Behavior worth keeping |
|---|---|---|
| Placeholder | `credential-proxy/proxy/placeholder.go` | Guest holds `agent-vault-<uuid>`. Proxy replaces that string in headers, query, and body |
| Domain binding | `authz/policies/credential_access.rego` | A credential is injectable only when `bound_domain` equals the request domain. Default is deny |
| CONNECT allow-list | `config.go` `IsAllowedDomain` | Exact, case-insensitive match. Unknown host rejects the CONNECT. Fail closed |
| Response scrub | `proxy/sanitizer.go` | Exact string replace of the real value with the placeholder. Request forces `Accept-Encoding: identity` so gzip does not hide the bytes |
| JWT stripped | `handlers.go` | `Proxy-Authorization` is removed before the upstream hop |
| Audit without the secret | Temporal local activity | Workflow history stores a request id and a count. The live `*http.Request` stays in process memory |
| Network lockdown | `modules/openclaw-vm/default.nix` `credproxy-lockdown` | New guest egress dropped. VSOCK is not a TCP interface, so the proxy path still works |

Do not copy these as-is:

- **devMode virtiofs of the proxy state dir.** `guest.nix` mounts the host
  credproxy directory and copies `oidc-client.env` into the guest. That
  directory is also where the MITM CA private key is generated
  (`/var/lib/credproxy/ca/`, mode `0400` in the architecture note). A share
  of the directory is how a CA key or an OIDC client secret enters the
  guest. Publish `ca.crt` only, as a file.
- **Tailscale auth key via fw_cfg** (`credentialFiles` in
  `openclaw-vm/default.nix`). fw_cfg is readable by guest root. An auth key
  in the guest plus the lockdown's Tailscale accept-rule is a second
  egress path.
- **OpenBao VSOCK bridge to guest `localhost:8200`** in devMode. The guest
  then talks to the vault API. Strike's proxy, if built, is the only
  guest-facing socket.
- **The nftables table is a `forward` hook.** Packets to the host's bridge
  address are usually `input`, not `forward`. DNS allows in that table do
  not prove host `strike serve` (port 8787), SSH, or a container socket
  are unreachable. Peasant's probe exists because "by construction" was
  not a test.
- **Rego `has_roles`.** Any non-empty role list passes. That is weaker
  than "this agent may use this credential."
- **Scrub limits.** Replacement is exact. Base64, a split token, or a
  value past `MaxBodyBytes` (10 MiB; the body is replaced by the prefix,
  so the tail is dropped rather than forwarded unsanitized) will not be
  rewritten. Scrub is a backstop. Domain binding is the control.
- **TLA+ scope.** `credential-proxy/model/` checks `NoDoubleDecision`,
  handler termination, and registry cleanup. The README says secret
  non-leakage is by construction and is not a state in the model. Do not
  cite the model as a proof of the threat model.
- **Research stack.** `docs/research/*` surveys Keycloak, OpenBao,
  Temporal, CyberArk Secretless, Ory Oathkeeper, Peta, Octelium. Secretless
  is the same shape (broker holds the secret, client holds a placeholder).
  Strike already has a secret-ref broker for host subprocesses
  (`secret://env/NAME`, [secrets.md](../../secrets.md)). A VM does not
  require a new vault to reuse that idea.

**peasant-labs/infra** boots one Cloud Hypervisor VM per CI job. The
security probe (`runner-vm-security-probe.yml`) is the artifact to copy
in spirit. From the job's view it checks, without printing file contents
or env values:

- no host podman socket and no `/run/user/1000`
- the engine behind `/var/run/docker.sock` names itself `runner-vm*`
- no `runner-dispatcher` process, no `/run/systemd/private`, no
  `/run/credentials`, no sops paths, no key-like names under `/run` and
  `/var/lib` (bounded `find`, names only)
- no host home, no `/etc/nixos`, no `/var/lib/microvms`, no `/run/jit`
- TCP to the default gateway on 22, 2375, 2376, 4243 fails
- TCP to `1.1.1.1:443` succeeds, so a dead network cannot fake the negatives

Their threat model is a CI job that must reach the internet. The JIT
registration secret is delivered on a read-only virtiofs share **into**
the guest, because the runner has to present it. A shared virtiofs cache
is shared across jobs. The spike report calls out that cache and the JIT
share as the intended exceptions, and records isolation as "by
construction" until the probe runs.

Strike is the opposite on credentials: the guest must not receive a
GitHub token or a provider key in order to do its job. NAT egress that
the probe treats as the healthy control is, for Strike, a failure of the
default policy. Signed images are a separate control: `runner-image.yml`
keyless-signs `quay.io/peasant-labs/github-runner` with cosign, Fulcio
identity pinned to that workflow on `main`, digest recorded, tag not
trusted. That signs the **container image loaded inside** the VM, not the
NixOS guest. Both hashes belong on the audit event if Strike ever pulls
an image. A flake built on the host is pinned by its Nix output path
instead.

## What must never enter the guest

"Enter" means: process memory, disk image, virtiofs, 9p, fw_cfg,
cloud-init, kernel cmdline, vsock payload, or an env var of a process in
the guest.

| Secret | Where it lives now | Rule |
|---|---|---|
| Provider API keys and exchanged API keys | `~/.strike/auth.json` (`0600`), provider env. Wire type in `harness/providers/auth.Credential` (`apiKey`, `access`, `refresh`, `idToken`) | Host provider client only. Factory reads the store in-process (`harness/providers/factory`) |
| OAuth refresh and access tokens | same file | Same. A refresh token in the guest is a long-lived key |
| Gateway bearer | `~/.strike/daemon.token`, cookie `strike_serve_token` ([gateway.md](../../gateway.md)) | Host gateway only. The guest is not a client of `/v1` |
| GitHub tokens (`ghp_`, `github_pat_`, `GH_TOKEN`, app private keys) | user env, `bashSecrets`, `gh` keyring, `.git-credentials` | Not mounted, not injected into guest env. Authenticated git runs on the host |
| SSH private keys and `SSH_AUTH_SOCK` | user home; the socket path is in `BashMinimalEnvKeys` for host bash | Do not forward the host agent. Guest `HOME` is an empty guest directory |
| MCP `env` values, plugin secrets | server config on the host (`internal/integrate/mcp`) | MCP stays a host process started by the host engine |
| MITM CA private key, vault tokens, AppRole secret IDs | not in Strike today | If a proxy is added: host process memory only. Guest receives `ca.crt` |
| Session JSONL, audit log, daemon state | `~/.strike/` on the gateway host | Guest cannot append or truncate these. They are not a mount |
| Another workspace's directory | host filesystem | Not a mount |

`pkg/redact` does not make a mount safe. It scrubs credential shapes in
logs, tool results, and exports after the bytes exist
([secrets.md](../../secrets.md)). Patterns cover `sk-` / `sk-ant-` /
`xai-`, `ghp_` / `github_pat_`, Slack, `AKIA`, PEM, `Bearer`, labeled
assignments, and (tool output only) long high-entropy tokens. A key that
never enters the guest does not need to match a regex.

Placeholders may enter the guest. They must not use a live key prefix
(`sk-`, `ghp_`). nix-openclaw's `agent-vault-<uuid>` shape is fine.
`secret://env/NAME` strings may appear in config the model can see; the
resolved value must not.

Workspace files may enter the guest. That includes secrets the user
stored in the repo. Say so in the VM status the product angle displays.
The VM is not a reason to weaken write-time content guards on the host
path.

## Recommended design

### Where processes run

MVP: **engine on the host, untrusted processes in the guest.**

The host keeps the provider HTTP client, auth store, session log, audit
sink, MCP client, and permission service. The guest runs mediated bash
and, when the user opens one, a PTY. Model traffic never originates in
the guest, so the MVP does not need a credential proxy to protect
provider keys.

An in-guest engine ([#1290](https://github.com/jonathanung/strike/issues/1290)
leaves this open) puts `auth.json` and the provider client in the
untrusted kernel's reach unless every outbound call is proxied. That is
the XL shape. Do not start there.

`session.childIsolation` today is `off` / `shared` / `worktree` (git
worktrees under `.strike/worktrees/`). Leave it alone. A VM is a
stronger **sandbox** dial for where processes run, next to `off` /
`read-only` / `workspace-write`, not a third worktree mode. The
architecture note should name the config key. This note only requires
that the key cannot be set by the model, and that `worktree` still means
a git worktree.

Inside the guest, bwrap is optional depth. The boundary is the
hypervisor. `sandbox: off` on the host must not be reachable from a
process that was supposed to run in the guest.

### Mounts and workspace roots ([#1260](https://github.com/jonathanung/strike/issues/1260))

[#1260](https://github.com/jonathanung/strike/issues/1260) binds a session
to a workspace id, a directory, extra roots, and a permission mode.
Today the session does not record that id
([webui-planned.md](../../webui-planned.md)). When it does, the guest
mount set is exactly those directories after the same symlink-safe
canonicalize the host tools already use (`safefile`, `resolveInWorkspace`):

- one virtiofs tag per root, not the parent that contains sibling repos
- `session.childIsolation=worktree` mounts the child worktree, not the
  primary checkout, as the writable root
- no `~/.strike`, no `/nix/store` of the host, no `/run`, no docker or
  podman socket, no nix-daemon socket (`/nix/var/nix/daemon-socket`)
- guest `HOME` is a fresh directory on the guest disk

Host `/nix/store` over virtiofs is how nix-openclaw gets fast rebuilds
(`useVirtiofs` in `openclaw-vm/guest.nix`). The store is world-readable.
Anything that landed in a derivation (a secret baked into a build, a
neighbor's out path names) becomes guest-readable, and a writable shared
cache is the cross-job leak peasant accepted for CI. MVP bakes the guest
closure into the image. A read-only store share is a later, explicit
opt-in with the probe still forbidding the daemon socket.

Plan mode mounts those roots read-only and does not start a PTY. Today
`ModeLateRules` denies the `write` and `edit` tools only
(`harness/permission/permission.go`). Bash can still mutate the tree.
A VM that honors plan mode has to close that with the mount flag, not
with another tool-name rule.

### Mediated exec, approvals ([#1266](https://github.com/jonathanung/strike/issues/1266)), terminal ([#1279](https://github.com/jonathanung/strike/issues/1279))

[#1266](https://github.com/jonathanung/strike/issues/1266) is the approval
UI: mode chip, allow once / always / deny, deny rules that still apply
under yolo. Strike modes are `default`, `plan`, `soft-approve`,
`accept-edits`, `yolo` ([usage.md](../../usage.md)). There is no separate
`trust` mode. `ApplyMode` turns `Ask` into `Allow` for yolo (and for
edit/write under accept-edits). `Deny` stays `Deny`. `yolo` does not
disable the OS sandbox. `--dangerously-skip-permissions` does clear
`network.allow` for the session. That last behavior must not be copied
onto a VM allow-list.

| Mode | Mediated guest command | Mounts | VM egress list |
|---|---|---|---|
| `default` | Ask, as bash asks today | workspace roots, read-write | unchanged |
| `plan` | No PTY. Mediated shell only if the command cannot write; prefer no guest shell | read-only | unchanged |
| `soft-approve` | Same countdown as a host bash ask. Card says the process runs in the VM | unchanged | unchanged |
| `accept-edits` | Shell still asks. Auto-allow stays on host edit/write | unchanged | unchanged |
| `yolo` | Asks skipped. Explicit deny still denies | unchanged | unchanged |

The approval card shows the command and that the target is the guest, so
"allow always" cannot be read as a host-root grant. Allow-always patterns
stay in the existing ruleset (last match wins). They do not add mounts.

A raw terminal ([#1279](https://github.com/jonathanung/strike/issues/1279))
is a daemon PTY on the workspace, authenticated like every `/v1` route,
and not bound on a non-loopback listener without the gateway flags. For a
VM session the PTY is a process in the guest. The browser still talks
only to the gateway. The gateway brokers bytes over VSOCK. The guest does
not run SSH, and it does not listen on the TAP.

Opening that PTY is one permission decision of bash class, audited, with
a visible "you and the agent share this shell" state when both are
attached. Per-command asks do not apply to keystrokes. Plan mode does not
offer the PTY. Session end kills the PTY. It does not need to be the
moment the VM disk is deleted; the runtime note owns lifetime. Scrollback
is guest memory: do not snapshot it into the session export.

Pasting a token into that terminal puts the token in the guest. The UI
cannot stop that. The status line should say the shell is inside the VM.

### Egress, DNS, and a credential proxy

**MVP network: no NAT, no guest default route.** The probe's positive
control is the inverse of peasant's: a connect to `1.1.1.1:443` must
fail, and a connect to the host gateway address on 22, 8787, 2375, 2376
must fail. If those host connects succeed, the VM is not a boundary.

**Next network milestone, still without secret injection:** a host proxy
reachable only by VSOCK. Guest `HTTP(S)_PROXY` points at a guest-local
socat bridge to VSOCK, as in `modules/credential-proxy/guest.nix`. Env
vars are not the boundary (the agent can unset them). The host packet
filter drops or redirects TCP 80/443 and drops other TCP/UDP, on both
`input` and `forward`. The proxy's CONNECT list is Strike's
`network.allow` (host, `*.suffix`, IP, CIDR). Empty list means deny, not
"public unrestricted" — the opposite of today's empty `network.allow`,
and on purpose. Identity of the caller is the VSOCK CID the hypervisor
supplies. No OIDC client secret in the guest. A JWT is justified only if
the proxy is later reachable by a path that is not VSOCK.

The proxy repeats `isBlockedIP` (`harness/tool/webfetch.go`): loopback,
RFC1918, link-local, multicast, unspecified, CGNAT `100.64.0.0/10`,
benchmark `198.18.0.0/15`, and therefore the metadata address
`169.254.169.254`. IP literals are denied unless the allow-list names
them. DNS for allow-listed names is resolved on the host at connect time,
with the same rebinding care webfetch already takes (check the address
that will be dialed). The guest has no recursive resolver. A recursive
resolver on the bridge is a DNS exfil channel even when TCP is dropped.

**Credential injection, only when a guest process must call a service
that requires a secret.** Not the model provider in the MVP.

| Caller | MVP | Later, if the guest is the HTTP client |
|---|---|---|
| Model provider (Anthropic, OpenAI-compatible, Google, …) | Host `harness/providers` adds the real `Authorization` header. Guest has no key and no reason to call the provider | Placeholder in the guest request, `bound_domain` set from the provider base URL configured on the host. Guest cannot edit the binding |
| `git` / `gh` | Guest: unauthenticated HTTPS only, and only if the name is on the allow-list. SSH dropped (port 22 and `git@` URLs). Push, fetch with auth, and `gh` run as host tools | Placeholder in the HTTPS userinfo (`x-access-token:agent-vault-…@github.com`), bound to `github.com` and `api.github.com`. Host git must use an ephemeral in-memory config (`GIT_CONFIG_*`), never `git config` on the shared worktree, or the real token is written where the guest can read it |
| MCP stdio | Host process. Guest has no socket to it and no copy of its env | Keep it on the host. A stdio server in the guest has the guest's files and no extra secrets |
| MCP HTTP | Not from the guest in the MVP. Host engine calls it under the existing `mcp` permission (`server/tool`) | Same placeholder and domain binding as any other HTTPS API. Admission ([admission.md](../../admission.md)) still runs at bind time on the host |

Injection rules, taken from the reference proxy and tightened:

- Fail closed on domain. Exact match is the MVP; wildcards only if
  `network.allow` already has that shape, evaluated on the host.
- One credential, one bound domain. A GitHub token is not injectable into
  a request to any other host.
- Replace placeholders in header, query, and body. Do not forward
  `Proxy-Authorization`.
- Force `Accept-Encoding: identity`. Scrub exact secret strings on the
  response. Treat scrub failure as fail closed (drop the response).
- Log the placeholder id, the domain, the allow/deny, and the scrub
  count. Run that line through `pkg/redact` anyway.
- Cap the body. Truncation is safer than forwarding an unscanned tail.
- The guest never receives a vault token. The host resolves
  `secret://env/NAME` or the auth store at inject time, the same moment
  host bash does via `bashSecrets`.

`bashSecrets` on a VM session injects into **host** tools only. Applying
it to guest exec is a bug.

### Audit ([#1286](https://github.com/jonathanung/strike/issues/1286))

Today's sink (`internal/trust/audit`, [audit.md](../../audit.md)) is
append-only JSONL under `~/.strike/audit/`, mode `0600`, redacted, with
families for permission, sandbox deny, egress deny, content guard,
admission, hooks, secret-ref use (name hash only), and serve ops.
[#1286](https://github.com/jonathanung/strike/issues/1286) asks for a hash
chain and an export covering approvals, permission changes, shell,
config edits, and token rotation. It does not ask for Temporal.

VM events go to that sink, on the host, so the guest cannot rewrite them:

| Family | Fields (no secret values, no command bodies) |
|---|---|
| `vm.lifecycle` | start, stop, image digest, hypervisor |
| `vm.mount` | tag, host-path hash, ro/rw |
| `vm.egress` | allow or deny, domain or "ip-denied", CID |
| `vm.credential` | placeholder id, bound domain, decision, scrub count. Only after injection exists |
| `vm.terminal` | attach, detach, shared-or-not |

`secret_ref_use` already records class and name hash for host bash
injection. Reuse it for proxy injection. A hash chain, when #1286 lands,
covers these families. Do not stand up a second log. Temporal's value in
nix-openclaw is a queryable workflow history; Strike's audit export is
that product. Local-activity discipline (secret stays in memory, history
gets an id) is the part to copy if a workflow engine ever appears.

### Image trust

Pin what you boot. A guest built from the user's flake is pinned by the
Nix output hash. An image pulled from a registry is pinned by digest, and
verified against a sigstore identity the way
`peasant-labs/infra/.github/workflows/runner-image.yml` pins
`runner-image.yml@refs/heads/main`, with the digest and the identity
moving together. The digest is the `vm.lifecycle` field. A tag is not a
pin. Quay re-serializes manifests; peasant hashes the bytes the registry
actually serves. Any Strike pull-and-verify has to do the same or the
signature will not match.

### Negative probe

Ship a probe that runs as the guest user and fails closed, modeled on
`runner-vm-security-probe.yml`:

- forbidden paths absent: host home, `~/.strike`, `/run/credentials`,
  `/run/secrets`, nix-daemon socket, host container sockets,
  `/run/systemd/private`, the proxy state directory, `ca.key`
- no process in the guest can open the host gateway, SSH, or container
  API on the bridge address
- default policy: `1.1.1.1:443` fails
- when the proxy milestone is on: CONNECT to a name outside `network.allow`
  fails, CONNECT to `169.254.169.254` fails, and a connect that bypasses
  the proxy env still fails
- the probe prints paths and verdicts, never file contents or env values

Peasant's check that egress works is a control against a vacuous pass.
Strike's matching control is a crafted allow-list entry that must succeed
when, and only when, the proxy milestone is enabled.

## Escapes that remain

These stay true after the MVP. Document them on the VM status, do not
pretend the hypervisor removed them.

| Path | What crosses | MVP stance |
|---|---|---|
| Virtiofs / 9p of a workspace root | Everything under that root, including secrets the repo holds. Symlinks that point at a path that is also mounted resolve. Symlinks to an unmounted path do not become a host read | Accept. One root per tag. No parent mounts |
| Shared writable virtiofs (caches, `/nix/store` of the host) | Cross-session poison and read of other out paths | Off |
| virtiofsd | Guest-to-host bugs in the daemon. It runs on the host beside the secrets | TCB. Narrow shares. Keep the daemon updated. No share of the proxy state dir |
| VSOCK | Guest can connect to every VSOCK port the host listens on. A forwarder to docker, to the gateway, or to a vault is a full bypass. nix-openclaw's devMode OpenBao bridge is the example | Listen only for the proxy and the terminal broker. Firewall is not what protects VSOCK |
| nix-daemon socket | Guest builds and reads as the host nix user. Equivalent to code execution in that user | Never mounted. Guest-local nix, if the self-config note wants it, uses a guest daemon and a guest store |
| KVM and the device model | Virtio-net, virtio-blk, vhost, a fat QEMU device. Historical guest-to-host bugs | Minimal devices: block, vsock, one virtiofs, serial console. No GPU, no USB, no PCI passthrough, no `/dev/kvm` in the guest |
| Guest kernel | Fully attacker-controlled once the shell exists. Expected | The host kernel is the asset |
| Raw PTY | Every keystroke bypasses asks, bash preflight, and content guard | User-opened, audited at attach, off in plan mode |
| DNS, if a recursive resolver is reachable | Exfil of workspace secrets as queries, even when TCP is denied | No guest recursive DNS |
| Response scrub and `pkg/redact` | Encoded or novel token shapes | Backstops. Not the boundary |
| RAM snapshot, guest core, host swap of guest memory | Any secret that did enter the guest, plus workspace data | No RAM snapshots in the MVP. Disk snapshots are the runtime note's problem and must not include `~/.strike` |
| Prompt injection | Next tool call | Unchanged. See [threat-model.md](../../threat-model.md) |
| CPU side channels across guests | Not a VM escape, a neighbor issue | Out of scope until two tenants share a host on purpose |

Containers ([container.md](../../container.md), epic #547) share the host
kernel. nix-openclaw's architecture note is right that namespaces are a
privilege reduction, not a boundary against a guest that runs arbitrary
code and wants the proxy's memory. Peasant rejected Kata for the CI pool
and still put the job in a VM. Strike can keep containers for dev
environments. They are not this threat model's answer for "agent has a
shell and a network."

## MVP slice

In, for the first supported VM session:

- Host engine and host provider client. Guest has no auth store.
- Mount allow-list = workspace roots from the #1260 binding (the serve
  working directory until that binding exists). Plan mode is read-only.
- No nix-daemon socket, no SSH agent, no host store share, no secret
  fw_cfg.
- Guest network off, proved by the negative probe, including host port
  8787.
- Mediated exec through the existing permission service and audit
  families. `vm.lifecycle` and `vm.mount` events on the host sink.
- Terminal, if #1279 lands in the same timeframe: gateway WebSocket to a
  guest PTY, attach audited, off in plan mode.
- Image pinned by Nix output hash.

Out, until a later issue:

- In-guest engine.
- Keycloak, OpenBao, OPA, Temporal.
- Placeholder injection. Design it as above; do not block the no-network
  MVP on it.
- Guest NAT, Tailscale, shared caches, RAM snapshots, registry images
  that need cosign.
- macOS / Windows hosts. No KVM means this boundary does not exist there.
  bwrap / `sandbox-exec` stay the sandbox. The runtime note owns that
  split; the security consequence is that a "VM" on those hosts is a
  different, weaker product and must not be labeled as this one.

## Risks

| Risk | What goes wrong | Mitigation in this note |
|---|---|---|
| Secrets copied in so the guest can call the API | The boundary is theater | Host provider client. Probe for `auth.json` and token files |
| `yolo` or `--dangerously-skip-permissions` clears the VM allow-list | Isolation dial silently dropped | Those flags skip asks only. VM list stays |
| Forward-only nftables | Guest reaches host `strike serve` and uses the token if it can find one | Probe ports on the gateway address. Cover `input` and `forward` |
| Raw PTY treated as the agent shell | Approvals and content guard vanish | Mediated exec for the agent. PTY is an explicit user action |
| Host git writes a token into the shared `.git/config` | Guest reads it on the next command | In-memory git config on the host. Probe for credential files under the mount |
| devMode-style shares copied from nix-openclaw | CA key or vault token in the guest | Share `ca.crt` as a file, or share nothing |
| Scrub trusted as proof | Encoded secret returned to the model | Domain binding and "secret never sent to the guest" |
| XL control plane built first | Months of IdP and vault before any probe exists | MVP is mounts, egress off, and the probe |
| Cross-session virtiofs cache | Poisoned compiler, stolen artifacts | No shared writable mount |
| Calling the TLA+ model a security proof | Concurrency bugs are checked; exfil is not | Cite it only for the channel protocol, if that code is ported |

## Open questions

Recommended defaults are what an implementer should ship if the question
is still open at coding time.

1. **Engine in the guest or on the host?** Host. Revisit only after
   placeholder injection and the probe exist.
2. **Config key?** Do not overload `session.childIsolation`. Prefer a
   sandbox value the architecture note names (`vm` beside
   `workspace-write`). The model cannot set it.
3. **Adopt Keycloak, OpenBao, OPA, and Temporal?** No. VSOCK CID is the
   proxy principal. Strike auth store and `secret://` refs are the secret
   source. Rego only if domain-binding rules outgrow a Go check the size
   of `credential_access.rego`. Audit stays `internal/trust/audit`.
4. **Default guest network?** Off. First networked step is the VSOCK
   proxy with `network.allow` as a fail-closed CONNECT list, still
   without injection.
5. **Empty allow-list means unrestricted public, as it does today?** Not
   for the VM. Empty means deny.
6. **DNS?** No recursive resolver in the guest. Host resolves at dial
   time and applies `isBlockedIP`.
7. **GitHub and provider keys?** Host only. Guest git is anonymous HTTPS,
   and only after the proxy milestone. SSH agent is not forwarded.
8. **MCP?** Host process, called by the host engine. No MCP env in the
   guest.
9. **Does yolo widen the VM?** No. Deny rules still deny. Plan mode is a
   read-only mount and no PTY.
10. **Does `--dangerously-skip-permissions` clear the VM allow-list?** No,
    even though it clears session `network.allow` today.
11. **Terminal transport?** Gateway WebSocket, then VSOCK. No guest SSH.
    Same loopback and `--auth` rules as [gateway.md](../../gateway.md).
12. **Which directories are mounted?** The workspace directory and extra
    roots from #1260, each canonicalized, nothing above them. Until #1260
    lands, the serve working directory only.
13. **Host `/nix/store` share?** Off in the MVP. Bake the closure.
14. **Image signature?** Nix output hash for a locally built guest.
    Cosign digest plus a pinned Fulcio identity for anything downloaded.
15. **RAM snapshots?** Off.
16. **Shared caches across sessions?** Off.
17. **Tailscale or another management NIC in the guest?** Off.
18. **Who may turn the VM on?** The user, in config, with a confirm when
    the session is yolo or the gateway bind is not loopback. Not a tool
    the model can call.
19. **Self-config (guest rebuilds NixOS)?** The self-config note owns the
    mechanism. The security constraint: a guest rebuild cannot add a
    virtiofs tag, a VSOCK forward, a secret credentialFile, or a nix-daemon
    socket. Those are host config. A change to them is a permission
    decision on the host, outside the guest.
