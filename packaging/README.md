# Package manager templates

Templates for third-party package managers. The release workflow renders
them for every tag and uploads the result as the `packaging-<tag>` workflow
artifact. Nothing is pushed to an external tap or the AUR automatically:
that needs credentials this repository does not hold.

| Path | Target | Rendered as |
|---|---|---|
| `homebrew/strike.rb.in` | Homebrew tap (`jonathanung/homebrew-strike`) | `Formula/strike.rb` |
| `aur/PKGBUILD.in` | AUR package `strike-bin` | `PKGBUILD` (+ `.SRCINFO`) |

Scoop is not here yet: scoop installs native Windows binaries, and the
release does not build any (native Windows is unsupported; see README).

## Render locally

```sh
gh release download vX.Y.Z --pattern checksums.txt --dir /tmp/strike-rel
scripts/render-packaging.sh vX.Y.Z /tmp/strike-rel/checksums.txt /tmp/strike-pkg
```

The script fails if a tarball is missing from `checksums.txt` or a
placeholder is left unfilled. Placeholders are `@TAG@`, `@VERSION@`
(tag without `v`), `@PKGVER@` (AUR form, `-` replaced by `_`), and
`@SHA256_<OS>_<ARCH>@`.

## Manual publish

Publish stable tags only. The `v0.10.*` line and `-rc.N` tags are
pre-releases and should not reach a tap or the AUR.

1. Download the rendered files from the tag's Release workflow run:
   `gh run download <run-id> --name packaging-vX.Y.Z --dir /tmp/strike-pkg`
   (or render locally as above).
2. Homebrew: in a checkout of the tap repo, copy
   `homebrew/strike.rb` to `Formula/strike.rb`, then
   `brew audit --strict --online Formula/strike.rb && brew install --build-from-source Formula/strike.rb && brew test strike`.
   Commit and push to the tap.
3. AUR: in a clone of `ssh://aur@aur.archlinux.org/strike-bin.git`, copy
   `aur/PKGBUILD`, run `makepkg --printsrcinfo > .SRCINFO` and
   `makepkg -si` to check it installs, then commit both files and push.

To automate later, add a tap deploy token / AUR SSH key as repository
secrets and a publish job gated on stable tags.
