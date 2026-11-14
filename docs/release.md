# Verifying release assets

Every `v*` tag release is built by `.github/workflows/release.yml` and
published with three independent integrity checks:

| Check | Files | Proves |
|---|---|---|
| sha256 | `checksums.txt`, `desktop-checksums.txt` | The download is not corrupt. `install.sh` and `strike --upgrade` check this automatically. |
| Sigstore signature | `<asset>.sigstore.json` | The file was signed by this repository's release workflow for that tag. |
| SLSA build provenance | stored by GitHub (attestations API) | Which workflow, commit, and runner built the file. |

The signature and the attestation are keyless: the workflow signs with a
short-lived certificate from Sigstore's Fulcio CA, bound to the GitHub
Actions OIDC identity of the run. The signature is recorded in the public
Rekor transparency log. No long-lived signing key exists, so there is no
public key to download.

## Signed assets

CLI (every `v*` tag):

- `strike_<tag>_<os>_<arch>.tar.gz` + `strike_<tag>_<os>_<arch>.tar.gz.sigstore.json`
- `checksums.txt` + `checksums.txt.sigstore.json`

Desktop (pre-release tags only, see [install.md](install.md#desktop-installers-pre-release)):

- `strike-desktop_<tag>_*.{deb,AppImage,dmg}` + matching `.sigstore.json`
- `desktop-checksums.txt` + `desktop-checksums.txt.sigstore.json`

Every desktop and CLI asset also has a build provenance attestation.

## Verify a signature with cosign

Requires [cosign](https://docs.sigstore.dev/cosign/system_config/installation/) v3 or later.
The bundles use the Sigstore protobuf bundle format; on cosign v2.4.x add
`--new-bundle-format` to each `verify-blob` command below.

```sh
TAG=v0.11.0   # any tag released after signing landed (#581)
ASSET=strike_${TAG}_linux_amd64.tar.gz
gh release download "$TAG" --repo jonathanung/strike \
  --pattern "$ASSET" --pattern "$ASSET.sigstore.json"

cosign verify-blob "$ASSET" \
  --bundle "$ASSET.sigstore.json" \
  --certificate-identity "https://github.com/jonathanung/strike/.github/workflows/release.yml@refs/tags/${TAG}" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Expected output is `Verified OK`. Pin `--certificate-identity` to the exact
tag you downloaded: a signature made for a different tag, a branch, or a
fork fails this check.

To check many archives at once, verify `checksums.txt` with cosign, then
check the archives against it:

```sh
gh release download "$TAG" --repo jonathanung/strike \
  --pattern 'strike_*.tar.gz' --pattern 'checksums.txt*'
cosign verify-blob checksums.txt \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity "https://github.com/jonathanung/strike/.github/workflows/release.yml@refs/tags/${TAG}" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum -c checksums.txt   # macOS: shasum -a 256 -c checksums.txt
```

## Verify build provenance with gh

Requires a recent GitHub CLI (`gh attestation verify --help` must list
`--signer-workflow` and `--source-ref`).

```sh
gh attestation verify "$ASSET" \
  --repo jonathanung/strike \
  --signer-workflow jonathanung/strike/.github/workflows/release.yml \
  --source-ref "refs/tags/${TAG}"
```

The command fetches the attestation by the file's digest and checks that it
was produced by `release.yml` in this repository for that tag. Add
`--format json` to see the full SLSA predicate (commit SHA, workflow run,
runner).

## Notes

- Releases published before signing landed (#581) have only `checksums.txt`.
- `install.sh` and `strike --upgrade` still verify sha256 only. Signature
  and provenance checks are manual for now.
- The macOS desktop app is still not Apple-notarized. A Sigstore signature
  proves where the DMG came from; it does not satisfy Gatekeeper.
