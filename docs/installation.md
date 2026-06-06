# Installation

!!! warning "Pre-alpha"
    Direct downloads, `.deb`/`.rpm`, and the Docker image are published on every
    release ([Releases](https://github.com/orlandoburli/apiary/releases)). The
    Homebrew and Scoop channels activate with the first **stable** tag (`v0.1.0`).

## macOS / Linux — Homebrew

```sh
brew install --cask orlandoburli/tap/apiary
```

The cask strips the macOS Gatekeeper quarantine flag automatically, so the
binary runs without the "unidentified developer" prompt.

## Windows — Scoop

```sh
scoop bucket add orlandoburli https://github.com/orlandoburli/scoop-bucket
scoop install apiary
```

## Linux — deb / rpm

Download the package for your architecture from the
[latest release](https://github.com/orlandoburli/apiary/releases/latest):

```sh
sudo dpkg -i apiary_*_linux_amd64.deb      # Debian / Ubuntu
sudo rpm -i  apiary_*_linux_amd64.rpm      # Fedora / RHEL
```

`arm64` packages are published too — swap `amd64` for `arm64`.

## Docker

```sh
docker run --rm -v apiary-data:/data ghcr.io/orlandoburli/apiary:latest version
```

Multi-arch image (`linux/amd64`, `linux/arm64`). Apiary persists state in a
SQLite database — mount a volume at `/data` to keep it across runs.

## Direct download

Grab the archive for your OS/arch from the
[latest release](https://github.com/orlandoburli/apiary/releases/latest)
(`.tar.gz` for macOS/Linux, `.zip` for Windows), verify it against
`checksums.txt`, extract, and put `apiary` on your `PATH`.

!!! note "Unsigned binaries"
    Releases are not yet code-signed or notarized.

    - **macOS** — a directly downloaded binary is quarantined by Gatekeeper.
      Clear it with `xattr -dr com.apple.quarantine ./apiary`. (The Homebrew
      cask does this for you.)
    - **Windows** — SmartScreen may warn "unknown publisher". Choose
      *More info → Run anyway*.

    Code signing and notarization are on the roadmap.

## From source

See the [Development guide](development.md#clone-and-build) for building with
the Go toolchain.
