# Development Guide

## Prerequisites

| Tool | Version | Notes |
|---|---|---|
| [Go](https://go.dev/dl/) | 1.22+ | All source lives in `src/` |
| [Git](https://git-scm.com/) | any | |
| An agent CLI | any | e.g. [opencode](https://opencode.ai), for testing runners |
| A Plane account | any | For testing the Plane source adapter (free tier works) |

## Clone and build

```sh
git clone https://github.com/orlandoburli/apiary.git
cd apiary/src

go build ./...
```

Build the binary:

```sh
go build -o apiary ./cmd/apiary
./apiary --help
```

Or install it to your `$GOPATH/bin`:

```sh
go install ./cmd/apiary
apiary --help
```

## Project structure

```
apiary/
├── src/                    # Go source
│   ├── cmd/apiary/         # Binary entry point
│   ├── internal/
│   │   ├── cli/            # Cobra commands
│   │   ├── config/         # apiary.yaml parsing and validation
│   │   ├── daemon/         # Dispatcher, IPC socket server, status types
│   │   ├── model/          # Cell, RunRequest, RunResult, ActiveRun
│   │   ├── router/         # Rule matching engine
│   │   ├── runner/         # Runner adapter interface + registry
│   │   │   ├── cli/        # CLI subprocess runner (opencode, gemini, …)
│   │   │   └── script/     # Shell script runner
│   │   ├── source/         # Source adapter interface + registry
│   │   │   └── plane/      # Plane source adapter
│   │   └── tui/            # Bubble Tea terminal UI
│   ├── sdk/                # Public SDK for custom adapters (planned)
│   ├── go.mod
│   └── go.sum
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

Minimal working config (using the `script` runner for local testing without a real agent CLI):

```yaml
version: "1"

sources:
  - id: my-plane
    type: plane
    config:
      workspace: your-workspace-slug
      project: your-project-uuid
      api_key: ${PLANE_API_KEY}
    poll_interval: 30s
    filters:
      labels: [ai-ready]

workers:
  - id: echo-worker
    runner: script
    model: none
    runner_config:
      command: echo "would run agent on: $APIARY_CELL_TITLE"

routes:
  - id: default
    priority: 99
    match:
      source: my-plane
    worker: echo-worker

settings:
  concurrency: 1
  state_lock: false       # don't modify task state while testing
  result_comment: false   # don't post comments while testing
```

Set your API key:

```sh
export PLANE_API_KEY=your_key_here
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
cd src
go test ./...
```

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
