# Proposal: Binary Distribution

## Why

Apiary builds to a single self-contained Go binary (`apiary`, module `github.com/orlandoburli/apiary`). Today the only way to obtain it is to clone the repo and run `make build` / `make install` — there is no published artifact for any platform. This blocks adoption: a user who just wants to *run* Apiary has to install the Go toolchain, clone the source, and compile.

There is also no release automation. Tags are not turned into downloadable binaries, there are no checksums, and there is no package-manager path (`brew`, `scoop`, `winget`, `apt`/`yum`, Docker). The `Makefile` already bakes a version into the binary via `-ldflags -X internal/version.Version`, and `git describe --tags` is wired up — but nothing consumes tags to produce releases.

This change defines **where and how Apiary binaries are published** so that, on every tagged release, users on macOS, Linux, and Windows can install Apiary through the channel idiomatic to their platform.

---

## What Changes

| Area | Before | After |
|---|---|---|
| **Release artifacts** | None | Cross-platform archives + checksums on every `vX.Y.Z` tag |
| **Build matrix** | Local single-target `make build` | darwin/linux/windows × amd64/arm64, cross-compiled in CI |
| **macOS / Linux install** | `git clone && make install` | `brew install orlandoburli/tap/apiary` |
| **Windows install** | `git clone && make install` | `scoop install apiary` / `winget install apiary` |
| **Linux native** | — | `.deb` / `.rpm` packages + AUR (publish) |
| **Containers** | — | OCI image on `ghcr.io/orlandoburli/apiary` (multi-arch) |
| **Release process** | Manual | GoReleaser driven by a tag-push GitHub Actions workflow |
| **Versioning** | `git describe` at local build | Semver tags are the single source of truth |

---

## Distribution Channels (v1 scope)

| Channel | Platforms | Mechanism | Repo / Target |
|---|---|---|---|
| **GitHub Releases** | all | GoReleaser archives (`.tar.gz` for unix, `.zip` for windows) + `checksums.txt` | this repo's Releases |
| **Homebrew tap** | macOS, Linux | GoReleaser `homebrew_casks` block writes a cask (`brews`/formula is deprecated) → `brew install --cask orlandoburli/tap/apiary` | new `orlandoburli/homebrew-tap` repo |
| **Scoop bucket** | Windows | GoReleaser `scoops` block writes a manifest | new `orlandoburli/scoop-bucket` repo |
| **winget** | Windows | GoReleaser `winget` block opens a `winget-pkgs` PR | upstream `microsoft/winget-pkgs` |
| **Linux packages** | Linux | GoReleaser `nfpms` produces `.deb` + `.rpm`, attached to the Release | this repo's Releases |
| **AUR** | Arch Linux | GoReleaser `aurs` block publishes a `PKGBUILD` | new `apiary-bin` AUR package |
| **Docker / OCI** | linux/amd64, linux/arm64 | GoReleaser `dockers` + `docker_manifests`, multi-arch | `ghcr.io/orlandoburli/apiary` |
| ~~**`go install`**~~ | all (with Go) | **deferred** — broken by the dual-`go.mod` layout (the module lives in `src/`); see design OQ #5 | not documented for now |

All channels are fed from a **single `.goreleaser.yaml`**, so one tag push produces every artifact consistently. The binary itself is unchanged; this is purely packaging and release plumbing.

---

## What Stays

- **The binary** — same `cmd/apiary` entrypoint, same flags, same behavior. No code change to the application.
- **`Makefile`** — local `build` / `install` / `test` targets stay for development. GoReleaser is for *releases*, not local dev.
- **Version injection** — keeps using `-ldflags -X github.com/orlandoburli/apiary/internal/version.Version`; GoReleaser is configured to set the same variable so local and released binaries report version identically.
- **`pages.yml`** — the docs workflow is untouched; the release workflow is a new, separate file.

---

## Cost

**v1 as specced is $0.** Apiary is an open-source public repo, so every channel and the CI itself are free:

| Item | Cost | Notes |
|---|---|---|
| GitHub Releases, archives, checksums | Free | |
| Homebrew tap / Scoop bucket (GitHub repos) | Free | |
| winget submission (`microsoft/winget-pkgs` PR) | Free | |
| AUR `apiary-bin` | Free | |
| ghcr.io container packages | Free | public packages are free |
| GitHub Actions CI (GoReleaser) | Free | **unlimited minutes for public repos**; only private repos burn quota |

Shipping **unsigned** macOS and Windows binaries costs nothing. The tradeoff is UX friction, not money — Gatekeeper ("unidentified developer") on macOS and SmartScreen ("unknown publisher") on Windows. Homebrew installs largely sidestep Gatekeeper (brew strips the quarantine attribute); direct GitHub-Release downloads do not.

**Cost only appears in the deferred signing change (below):**

| Platform | Purchase | Rough cost |
|---|---|---|
| macOS notarization | Apple Developer Program (Developer ID cert + `notarytool`) | **$99/year** flat |
| Windows signing | Authenticode cert from a CA; post-2023 OV needs a hardware token / cloud HSM, EV gives instant SmartScreen reputation | **OV ~$200–400/yr**, **EV ~$300–600/yr** (+ possible HSM fee) |

Signing is independently adoptable: macOS-only ($99/yr) is a reasonable first step, leaving Windows unsigned until SmartScreen friction justifies the larger spend.

---

## Out of Scope (deferred to follow-up changes)

- **macOS notarization** (Apple Developer ID + `notarytool`) and **Windows Authenticode signing**. v1 ships **unsigned**. The docs MUST call out the Gatekeeper ("unidentified developer") and SmartScreen friction and the workaround (`xattr -d com.apple.quarantine`, "Run anyway"). Signing requires paid certificates and CI secret management — its own change.
- **Homebrew core** / **winget defaults beyond the manifest PR** — we start with our own tap/bucket; promotion to upstream catalogs comes later.
- **SLSA provenance / Sigstore cosign attestation** — desirable for supply-chain integrity; noted as a fast follow once the pipeline is stable.
- **Linux Snap / Flatpak / Nix derivation** — additional Linux channels can be layered on the same GoReleaser config later.

---

## Release Flow (target)

```
git tag vX.Y.Z && git push --tags
        │
        ▼
GitHub Actions (.github/workflows/release.yml, on: push tags 'v*')
        │  goreleaser release --clean
        ▼
┌───────────────────────────────────────────────────────────┐
│ build matrix: darwin/linux/windows × amd64/arm64           │
│ → archives + checksums.txt        → GitHub Release         │
│ → formula                         → homebrew-tap repo      │
│ → manifest                        → scoop-bucket repo      │
│ → winget manifest                 → winget-pkgs PR         │
│ → .deb / .rpm                     → GitHub Release assets  │
│ → PKGBUILD                        → AUR                    │
│ → multi-arch image + manifest     → ghcr.io                │
└───────────────────────────────────────────────────────────┘
```

The only manual step is pushing a semver tag. Everything downstream is automated and reproducible.
