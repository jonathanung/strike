# Nix dev environment

This project provides a [Nix flake](https://nixos.wiki/wiki/Flakes) at the repo
root. It exposes an installable `strike` package and an isolated development
shell with the exact Go 1.26, gopls, make, and git versions pinned in
`flake.lock`.

## Philosophy

Nix is a **purely functional package manager** and **build system**. Two
principles define it:

1. **Reproducibility** — every build is a pure function of its inputs. Running
   `nix develop` on the same flake.lock hash on any machine produces identical
   tooling. No "works on my machine."
2. **Declarative isolation** — dependencies are stated explicitly, not assumed
   from the host environment. The flake is the single source of truth for what
   is available inside the dev shell. Nothing leaks from your globally installed
   Go or homebrew.

These two properties make Nix a natural fit for development environments:

- No version drift across a team.
- No conflicts between project requirements (project A needs Go 1.25, project B
  needs Go 1.26).
- CI can use the same derivation as your laptop shell. No Docker image needed
  for tooling.

## Key concepts

| Concept | Role |
|---|---|
| **Store** (`/nix/store`) | Immutable content-addressed cache of every package, derivation, and built artifact. Nothing mutates in place. |
| **Derivation** (`.drv`) | A build recipe: inputs + build script = output hash. The fundamental unit. |
| **Nixpkgs** | The main package repository, itself a Git repo pinned by `flake.lock`. |
| **Flake** (`flake.nix` + `flake.lock`) | A self-contained Nix project with locked inputs. The standard way to share packages and dev shells. |
| **Dev shell** (`devShells.default`) | A `nix develop` environment: specific tools, environment variables, and hooks — isolated from the host. |
|

## Best practices

This project follows these conventions, drawn from the wider Nix ecosystem.

- **Pin nixpkgs via flake.lock** — the lockfile records exact Git revisions of
  every input so you and CI get the same Go compiler. Update intentionally:
  `nix flake update`.
- **Keep devShell packages minimal** — only tools the project needs in the
  shell: `go`, `gopls`, `gnumake`, `git`. Everything else comes from
  `go mod download`.
- **Use shellHook for project-local paths** — `GOPATH`, `GOCACHE`, `GOENV` are
  scoped to `.nix-go/` inside the repo. Keep hooks small; don't patch or
  install ad-hoc tooling from a hook.
- **Prefer overlays over manual patches** — overlays compose safely and pass
  `nix flake check`. This project doesn't need any yet.
- **Run `nix flake check`** before committing — validates the flake schema,
  evaluates all outputs, catches syntax errors early.

## Reference projects

These well-established Go and Nix projects use the same patterns:

| Project | What to look at |
|---|---|
| [devenv](https://github.com/cachix/devenv) (by Cachix) | Composable, module-based dev shell framework built on Nix. See their [flake.nix](https://github.com/cachix/devenv/blob/main/flake.nix) for how they manage a complex multi-input flake with overlays. |
| [flake-parts](https://github.com/hercules-ci/flake-parts) (by Hercules CI) | The module-system approach to flake composition. Their [own flake](https://github.com/hercules-ci/flake-parts/blob/main/flake.nix) is a reference for self-hosting with partitions, checks, and templates. |
| [nixpkgs Go infrastructure](https://github.com/NixOS/nixpkgs/blob/master/pkgs/development/go-modules/gomod2nix) | `buildGoModule` builds the installable `strike` package; the dev shell continues to use `go build` directly. |
| [golangci-lint flake](https://github.com/golangci/golangci-lint/blob/master/flake.nix) | A Go project with flakes for CI and development. Clean example of a minimal Go dev shell. |

## How it works in this project

1. `flake.nix` declares the `strike` package, app, and dev shell with
   nixpkgs-unstable + flake-utils.
2. `flake.lock` pins the exact nixpkgs revision (update with `nix flake update`).
3. `nix develop` drops you into a shell with `GOPATH`, `GOCACHE`, and `GOENV`
   scoped to `.nix-go/` inside the repo — no cross-project contamination.
4. `make build`, `make test`, `make run-echo` all work inside the shell.

The shell sets `GOTOOLCHAIN=local` so Go uses the Nix-provided compiler rather
than auto-downloading a newer one from the internet.
