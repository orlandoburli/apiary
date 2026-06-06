# Tasks: Binary Distribution

Read `proposal.md` and `design.md` first. The cgo cross-compilation decision (Phase 0) gates everything after it.

**Key references:**
- `design.md` — build matrix, cgo strategies, `.goreleaser.yaml` shape, release workflow, secrets
- `proposal.md` — channels, scope, what's deferred

---

## Phase 0 — Decisions (no code)

- [ ] 0.1 Decide cgo cross-compile strategy: A (`goreleaser-cross`), B (native-runner matrix), or C (`zig cc`) — see design "cgo cross-compilation"
- [ ] 0.2 Decide whether to first migrate `mattn/go-sqlite3` → `modernc.org/sqlite` (pure-Go) to avoid cgo cross-compile entirely — spike + test; if yes, it's a precursor change
- [ ] 0.3 Decide v1 channel cut: confirm brew + scoop + releases + nfpm + docker; mark winget / AUR as v1 or fast-follow
- [ ] 0.4 Decide `latest` Docker tag policy (stable-only vs every tag)

## Phase 1 — Release pipeline core (GitHub Releases)

Minimum shippable: tag → cross-platform archives + checksums on the GitHub Release.

- [ ] 1.1 Add `.goreleaser.yaml` with `builds`, `archives`, `checksums`, `release` blocks per chosen cgo strategy; ldflags set `internal/version.Version` to match the Makefile
- [ ] 1.2 Add `.github/workflows/release.yml` (`on: push tags 'v*'`, `contents:write`), running `goreleaser release --clean`
- [ ] 1.3 Add a PR check job: `goreleaser check` + `goreleaser release --snapshot --clean` (no publish) so config breakage is caught pre-tag
- [ ] 1.4 Dry-run: cut a `v0.x.0-rc` pre-release; download each archive, run `apiary version`, confirm it prints the tag

## Phase 2 — Linux native packages

- [ ] 2.1 Add `nfpms` block (deb + rpm) to `.goreleaser.yaml`; attach to the Release
- [ ] 2.2 Smoke: `dpkg -i` / `rpm -i` the artifacts in a container, run `apiary version`

## Phase 3 — Containers (ghcr.io)

- [ ] 3.1 Add `Dockerfile` (or `ko`/buildx config) and `dockers` + `docker_manifests` blocks (linux/amd64 + arm64)
- [ ] 3.2 Add ghcr.io login + `packages:write` to `release.yml`; push multi-arch manifest
- [ ] 3.3 Make the ghcr package public; smoke `docker run ghcr.io/orlandoburli/apiary:<tag> version`

## Phase 4 — Homebrew + Scoop

- [ ] 4.1 Create `orlandoburli/homebrew-tap` and `orlandoburli/scoop-bucket` repos
- [ ] 4.2 Create fine-grained PAT `TAP_GITHUB_TOKEN` (contents:write on the tap/bucket repos); add as Actions secret
- [ ] 4.3 Add `brews` + `scoops` blocks; tag a release; verify `brew install orlandoburli/tap/apiary` (macOS + Linux) and `scoop install apiary`

## Phase 5 — winget + AUR (fast-follow if deferred in 0.3)

- [ ] 5.1 Fork `microsoft/winget-pkgs`; add `winget` block; verify the auto-opened PR
- [ ] 5.2 Create AUR `apiary-bin`, register `AUR_KEY` deploy key, add `aurs` block; verify `PKGBUILD` publishes

## Phase 6 — Documentation

- [ ] 6.1 Rewrite README install section: per-platform commands (brew/scoop/winget/apt/rpm/docker/`go install`)
- [ ] 6.2 Move clone-and-build instructions to `DEVELOPMENT.md`
- [ ] 6.3 Add mkdocs **Install** page mirroring the README (update `mkdocs.yml` nav)
- [ ] 6.4 Document the unsigned-binary Gatekeeper / SmartScreen workaround prominently (until signing lands)

## Phase 7 — Deferred (separate changes — do not implement here)

- [ ] 7.1 macOS notarization (Developer ID + notarytool) and Windows Authenticode signing
- [ ] 7.2 SLSA provenance / cosign attestation
- [ ] 7.3 Homebrew core + additional Linux channels (Snap/Flatpak/Nix)
