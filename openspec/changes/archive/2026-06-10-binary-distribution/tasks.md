# Tasks: Binary Distribution

Read `proposal.md` and `design.md` first. The cgo cross-compilation decision (Phase 0) gates everything after it.

**Key references:**
- `design.md` — build matrix, cgo strategies, `.goreleaser.yaml` shape, release workflow, secrets
- `proposal.md` — channels, scope, what's deferred

---

## Phase 0 — Decisions (no code)

- [x] 0.1 ~~Decide cgo cross-compile strategy~~ — **moot.** Chose to remove cgo entirely (0.2), so no osxcross/mingw/zig needed.
- [x] 0.2 Migrate `mattn/go-sqlite3` → `modernc.org/sqlite` (pure-Go) — **done.** Isolated to `client.go` (driver name, `?_pragma=busy_timeout(5000)` DSN, `*sqlite.Error.Code()==SQLITE_CONSTRAINT_UNIQUE`); full test suite green; all 6 targets cross-compile with `CGO_ENABLED=0`.
- [x] 0.3 v1 channel cut: brew + scoop + releases + nfpm + docker in v1; winget / AUR as fast-follow (Phase 5)
- [x] 0.4 `latest` Docker tag policy: **stable-only** — the `:latest` manifest uses `skip_push: auto`, so prereleases/snapshots never move `:latest`

## Phase 1 — Release pipeline core (GitHub Releases)

Minimum shippable: tag → cross-platform archives + checksums on the GitHub Release.

- [x] 1.1 Add `.goreleaser.yaml` with `builds`, `archives`, `checksums`, `release` blocks; ldflags set `internal/version.Version` to match the Makefile
- [x] 1.2 Add `.github/workflows/release.yml` (`on: push tags 'v*'`, `contents:write`), running `goreleaser release --clean`
- [x] 1.3 Add a PR check job (`release-check.yml`): `goreleaser check` + `goreleaser release --snapshot --clean` (no publish) so config breakage is caught pre-tag
- [x] 1.4 Dry-run done: **`v0.1.0-rc.1`** released live — GitHub prerelease with 11 assets (6 archives + 4 deb/rpm + checksums); binary reports the injected version

## Phase 2 — Linux native packages

- [x] 2.1 Add `nfpms` block (deb + rpm) to `.goreleaser.yaml`; attach to the Release
- [x] 2.2 Smoke: snapshot built deb+rpm for amd64+arm64 *(container `dpkg -i`/`rpm -i` install test still TODO on the real release)*

## Phase 3 — Containers (ghcr.io)

- [x] 3.1 Add `Dockerfile` (distroless/static, nonroot) and `dockers` + `docker_manifests` blocks (linux/amd64 + arm64). **Note:** `dockers`/`docker_manifests` are deprecated in favor of `dockers_v2` — migrate later (Phase 7-ish).
- [x] 3.2 Add ghcr.io login + QEMU + buildx + `packages:write` to `release.yml`; multi-arch manifest (`:latest` stable-only via `skip_push: auto`)
- [x] 3.3 Images + manifest pushed live by `v0.1.0-rc.1` (tag is `0.1.0-rc.1`, no `v`). ghcr package flipped to **public** — anonymous pull verified (tags listed through `0.4.0` + `latest`)

## Phase 4 — Homebrew + Scoop

- [x] 4.1 **(user)** Create `orlandoburli/homebrew-tap` and `orlandoburli/scoop-bucket` repos (public) — both exist and are public
- [x] 4.2 **(user)** Create fine-grained PAT `TAP_GITHUB_TOKEN` (contents:write on the tap/bucket repos); add as Actions secret — done (stable releases published to both via the pipeline)
- [x] 4.3 Add `homebrew_casks` + `scoops` blocks (config landed ahead of the repos/token); wire `TAP_GITHUB_TOKEN` into `release.yml` (+ dummy in `release-check.yml`). `skip_upload: auto` → only stable tags publish. **Note:** `brews` (formula) is deprecated → used `homebrew_casks`, so install is `brew install --cask orlandoburli/tap/apiary`. Cask includes an `xattr` postflight to strip Gatekeeper quarantine (unsigned binary). Validated: snapshot writes `Casks/apiary.rb` (per-arch URLs+sha) and `scoop/apiary.json`.
- [x] 4.4 After 4.1+4.2: cut a **stable** tag (`v0.1.0`), then verify `brew install --cask orlandoburli/tap/apiary` (macOS + Linux) and `scoop install apiary` — v0.1.0 through v0.4.0 shipped; brew/scoop channels live

## Phase 5 — winget + AUR — DROPPED

Decision (2026-06-06): **not pursued.** Windows is served by Scoop + direct
download + WSL; Linux by deb/rpm + Docker + Homebrew. The extra setup cost
(winget-pkgs fork + Microsoft review; AUR account + SSH deploy key) isn't worth
it for the added coverage. Can be revived later if demand appears.

- [~] 5.1 ~~winget~~ — dropped (Scoop + direct download cover Windows)
- [~] 5.2 ~~AUR~~ — dropped (deb/rpm + Docker + Homebrew cover Linux)

## Phase 6 — Documentation

- [x] 6.1 README **Installation** section: brew cask / scoop / deb / rpm / docker / direct-download. **`go install` omitted** (dual-go.mod, OQ #5 decision); **winget omitted** until Phase 5 lands. Pre-alpha note: packaged channels go live at the first stable tag.
- [x] 6.2 Clone-and-build already lives in `DEVELOPMENT.md` (#clone-and-build) — README links to it instead of duplicating.
- [x] 6.3 `docs/installation.md` added + `mkdocs.yml` nav (after Home); validated via `mkdocs build`. Added a `DEVELOPMENT.md`→`development.md` sed rewrite to `pages.yml` for the copied README link.
- [x] 6.4 Unsigned-binary Gatekeeper (`xattr -dr com.apple.quarantine`) + SmartScreen ("Run anyway") workaround documented in both README and the Install page.

## Phase 7 — Deferred (separate changes — do not implement here)

- [ ] 7.1 macOS notarization (Developer ID + notarytool) and Windows Authenticode signing
- [ ] 7.2 SLSA provenance / cosign attestation
- [ ] 7.3 Homebrew core + additional Linux channels (Snap/Flatpak/Nix)
