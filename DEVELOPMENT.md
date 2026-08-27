# Development Guide

## Prerequisites

| Tool | Version | Notes |
|---|---|---|
| [Go](https://go.dev/dl/) | 1.22+ | All source lives in `src/` |
| [Git](https://git-scm.com/) | any | |
| `make` | any | GNU Make or compatible |
| An agent CLI | any | e.g. [opencode](https://opencode.ai), for testing runners |
| A Plane account | any | For testing the Plane source adapter (free tier works) |

## Clone and build

```sh
git clone https://github.com/orlandoburli/apiary.git
cd apiary
```

```sh
make build      # compile → bin/apiary
make install    # compile + install to $GOPATH/bin (puts apiary on your PATH)
```

Run `make` or `make help` to see all available targets:

```
  build             Build the apiary binary into bin/
  install           Install apiary to $GOPATH/bin (makes it available on PATH)
  test              Run all tests
  test-verbose      Run all tests with per-test output
  test-cover        Run tests and open an HTML coverage report
  check             Build + test (use in CI)
  tidy              Run go mod tidy
  vet               Run go vet
  clean             Remove build artifacts
  help              Show available targets
```

> **Note on `go build ./...`** — you may see this pattern in Go docs. It compiles
> every package in the module but produces no output file; it's used purely as a
> compile-error check. `make build` is the command that actually produces the
> `bin/apiary` binary.

## Project structure

```
apiary/
├── Makefile                # build, test, install targets
├── src/                    # Go source
│   ├── cmd/apiary/         # Binary entry point
│   ├── internal/
│   │   ├── cli/            # Cobra commands
│   │   ├── config/         # apiary.yaml parsing and validation
│   │   ├── daemon/         # Dispatcher, IPC socket server, status types
│   │   ├── model/          # task model, RunRequest, RunResult, ActiveRun
│   │   ├── router/         # Rule matching engine
│   │   ├── runner/         # Runner adapter interface + registry
│   │   │   ├── execution/  # cli + api execution engines
│   │   │   └── providers/  # claude, opencode, cursor presets
│   │   ├── source/         # Source adapter interface + registry
│   │   │   ├── github/     # GitHub Issues source adapter
│   │   │   └── plane/      # Plane source adapter
│   │   └── tui/            # Bubble Tea terminal UI
│   ├── go.mod              # daemon module (replaces the SDK with ../sdk)
│   └── go.sum
├── sdk/                    # Public plugin SDK — its OWN Go module
│   │                       #   github.com/orlandoburli/apiary/sdk
│   │                       #   stdlib-only, tagged sdk/vX.Y.Z
│   ├── plugin/             # protocol envelope + source wire types
│   └── go.mod
└── openspec/               # Specifications (proposal / design / tasks)
    ├── CHANGELOG.md
    ├── specs/              # Canonical specs per topic
    └── changes/            # Active and archived change records
```

## Run locally

### 1. Create a config file

```sh
cd /your/project
apiary init          # scaffolds apiary.yaml — edit it before running
```

Minimal working config — a GitHub source routed to a single Claude agent:

```yaml
version: "1"

runners:
  - id: claude
    type: cli
    provider: claude
    config:
      args: ["--output-format", "stream-json", "--verbose"]

sources:
  - id: my-repo
    type: github
    config:
      repo: my-org/my-repo
      api_key: ${GITHUB_TOKEN}
    poll_interval: 30s
    filters:
      labels: [ai-ready]

agents:
  - id: engineer
    description: "Implements tasks"
    runner: claude
    model: claude-sonnet-4-6

workflows:
  - id: default
    trigger:
      priority: 99
      match:
        source: my-repo
    steps:
      - id: run
        agent: engineer

settings:
  concurrency: 1
  state_lock: false       # don't modify task state while testing
  result_comment: false   # don't post comments while testing
```

Create a `.env` file next to your `apiary.yaml` with your secrets:

```sh
# .env  (never committed — already in .gitignore)
GITHUB_TOKEN=your_token_here
```

Apiary loads `.env` automatically on every command. Already-set shell variables always take precedence. To point at a different file:

```sh
apiary run --env-file /path/to/secrets.env
```

### 2. Validate the config

```sh
apiary validate
```

### 3. Run the daemon

```sh
apiary run
```

This opens the TUI. The dispatcher starts polling in the background.

In another terminal:

```sh
apiary status           # one-shot status
apiary status --watch   # refresh every 2s
```

### 4. One-shot mode (no TUI, good for testing)

```sh
apiary run --once --dry-run   # connects to sources, matches tasks, does not invoke runners
apiary run --once             # polls once, dispatches all matching tasks, exits
```

`--once` exits with code `4` if any run failed — useful in CI:

```sh
apiary run --once && echo "all runs succeeded"
```

## Run tests

```sh
make test              # run all tests
make test-verbose      # run all tests with per-test output
make test-cover        # run tests + open HTML coverage report in browser
```

To run a specific package or test directly:

```sh
cd src
go test ./internal/router/...
go test -run TestRoute_FirstMatchWins ./internal/router/...
```

### What is tested

| Package | Coverage |
|---|---|
| `internal/router` | Rule matching — source, labels, type, priority, regex, ordering, fallthrough |
| `internal/config` | Validation — missing fields, duplicate IDs, dangling references |
| `internal/source/plane` | Comment formatting, HTML escaping, filter logic, cell mapping |
| `internal/runner/script` | Real subprocess execution, env injection, failure detection, log streaming |

Packages that talk to external systems (Plane API, agent CLIs) are not covered by unit tests — use `--dry-run` and `--once` for integration testing against a real environment.

## Adding a new source adapter

1. Create `src/internal/source/<name>/adapter.go`
2. Implement `source.Adapter` (see [plugin API spec](openspec/specs/plugin-api/spec.md))
3. Register it in `init()`:

```go
func init() {
    source.Register("myname", func() source.Adapter { return &Adapter{} })
}
```

4. Blank-import it in `src/cmd/apiary/main.go`:

```go
_ "github.com/orlandoburli/apiary/internal/source/myname"
```

## Adding a new runner adapter

Same pattern under `src/internal/runner/<name>/runner.go`, implementing `runner.Adapter`.

## Environment variables

| Variable | Default | Description |
|---|---|---|
| `APIARY_CONFIG` | `./apiary.yaml` | Default config file path |
| `APIARY_SOCKET` | `~/.apiary/apiary.sock` | IPC socket path |
| `APIARY_LOG_LEVEL` | `info` | Log verbosity |
| `APIARY_DRY_RUN` | `false` | Global dry-run override |

## Useful commands

```sh
# validate config
apiary validate

# scaffold a new apiary.yaml in the current directory
apiary init

# dispatch a specific task manually (bypasses routing rules)
apiary dispatch --cell my-plane/<task-uuid> --worker echo-worker

# list tasks visible to Apiary right now
apiary cells

# manage the background service
apiary service install    # install as systemd / launchd / Windows Service
apiary service start
apiary service stop
apiary service uninstall
```
