# sterm — Simplified Kubernetes Terminal TUI

sterm is a lightweight, keyboard-driven Kubernetes management TUI built with
`tcell/v2` and `k8s.io/client-go`. It provides a terminal interface for browsing
cluster resources, inspecting YAML/descriptions, streaming logs, editing
resources, and exec'ing into containers — all without leaving the terminal.

## Features

### Browser (Resource Types)
| Key | Action |
|---|---|
| `j`/`k` / `↑`/`↓` | Navigate resource types |
| `g` / `Home` | Go to top |
| `G` / `End` | Go to bottom |
| `Enter` | Enter resource list |
| `n` | Switch namespace |
| `c` | Switch context |
| `p` | Jump to pods |
| `/` | Search (kind/resource/shortname) |
| `?` | Help page |
| `Ctrl+Q` | Quit |

### List (Resource Instances)
| Key | Action |
|---|---|
| `j`/`k` / `↑`/`↓` | Navigate rows |
| `Enter` | View YAML |
| `d` | Describe (kubectl describe) |
| `y` | View YAML |
| `e` | Edit resource ($EDITOR) |
| `s` | Interactive shell (pods only) |
| `l` | Stream logs (pods only) |
| `x` | Delete pod (confirm y/N) |
| `/` | Filter rows |
| `n` | Switch namespace |
| `c` | Switch context |
| `ESC` | Back to browser |

### Detail (YAML / Describe)
| Key | Action |
|---|---|
| `j`/`k` / `↑`/`↓` | Scroll |
| `g` / `Home` | Go to top |
| `G` / `End` | Go to bottom |
| `/` | Search text |
| `n`/`N` | Jump to next/prev match |
| `d` | Toggle YAML / Describe |
| `e` | Edit ($EDITOR) |
| `ESC` | Back to list |

### Shell (Embedded Terminal)
| Key | Action |
|---|---|
| Typing | Direct input to container shell |
| `Enter` | Newline |
| `Backspace` | Delete character |
| `Ctrl+C` | Interrupt (SIGINT) |
| `Ctrl+W`/`U`/`K`/`A`/`E` etc. | Standard shell shortcuts |
| `ESC` | Close shell |

### Pod Status Colors
| Color | Meaning |
|---|---|
| 🔴 Red | Error, Terminating, CrashLoopBackOff, ErrImagePull |
| 🟡 Yellow | Pending, Evicted, Init:xxx, ContainerCreating |
| Default | Running, Succeeded, Completed |

### Node Management
| Key | Action |
|---|---|
| `o` | Cordon (disable scheduling) |
| `u` | Uncordon (enable scheduling) |

### Local Terminal Popup (`Ctrl+J`)
- Opens a 1/3-screen local terminal in sterm host
- Inherits $SHELL (zsh/bash), environment, rc files, and themes
- 256-color and True color support for prompt themes
- Same key (`Ctrl+J`) toggles open/close
- Works from any page

## Quick Start

```bash
# Build
make build

# Run (uses current kubeconfig context)
./sterm

# Or run directly without building
make dev
```

The tool reads `$KUBECONFIG` or falls back to `~/.kube/config`, then
`InClusterConfig` for in-cluster use.

## Installation

```bash
git clone https://github.com/Micost/sterm.git
cd sterm
make build
sudo cp sterm /usr/local/bin/
```

## Requirements

- Go 1.22+
- A valid kubeconfig (or in-cluster config)
- `kubectl` on `$PATH` (for exec/shell and describe)

## Development

### Build commands

```bash
make build       # CGO_ENABLED=0 go build -o sterm .
make dev         # go run .
make clean       # rm -f sterm
make install     # sudo cp sterm /usr/local/bin/
make version     # print version info
make tag [v0.2]  # commit, tag, and push (auto-reads version from version.go)
```

### Testing

Three layers, from fast to full:

```bash
make test          # unit + fake client tests (no cluster needed)
make cover         # HTML coverage report
make integration   # full integration tests (needs k3s/k8s cluster)
```

**Unit tests** (`make test`) — pure logic, no dependencies:
`age()`, `extractStatus()`, `category()`, `isStandardGroup()`,
`matchesFilter()`, `columnWidth()`, `truncate()`.

**Fake client tests** (also `make test`) — in-memory K8s API, validates
List/Delete/conditional columns without a real cluster.

**Integration tests** (`make integration`) — runs against your local
k3s/k8s, creates and cleans up test resources automatically:

| Test | What it verifies |
|---|---|
| Discover | resource discovery, common types exist |
| CRUD | create → list → get → update → delete |
| ListPods | pod listing + NODE column |
| Describe | resource description output |
| Namespaces | namespace listing |
| Exec | exec into busybox, assert stdout |
| Logs | stream pod logs, assert content |

Integration tests use `//go:build integration` tag so they never run
during `make test` or in CI.

### Releasing

Releases are built and published by [GoReleaser](https://goreleaser.com) via
GitHub Actions, triggered automatically when a new tag is pushed.

**Step-by-step release process:**

1. Update the version in `version.go`:
   ```go
   var version = "v0.2.0"
   ```

2. Commit and tag:
   ```bash
   make tag v0.2.0
   ```

3. Push the tag (and commits):
   ```bash
   git push --follow-tags
   ```

4. GitHub Actions will build binaries for **linux/darwin** on **amd64/arm64**
   and create a GitHub Release with a checksums file.

**Download a release:**

```bash
# Replace {version}, {os}, {arch} as needed
curl -L -o sterm https://github.com/Micost/sterm/releases/download/v0.2.0/sterm_v0.2.0_linux_amd64
chmod +x sterm
sudo mv sterm /usr/local/bin/
```

### Project layout

```
main.go          Entry point: kubeconfig -> k8s client -> TUI App
version.go       Version/build info (ldflags injected)
pkg/
├── k8s/
│   ├── client.go       Typed + dynamic + discovery client wrapper,
│   │                   context listing, Exec via remotecommand
│   ├── resource.go     Discover() lists all cluster GVRs
│   ├── lister.go       List/Get/Delete/Update/ToYAML/Namespaces
│   ├── describe.go     KubectlDescribe() + legacy Describe()
│   ├── logs.go         StreamLogs() + PodContainers()
│   ├── *_test.go       Unit + fake client + integration tests
└── tui/
    ├── app.go          Multi-page TUI (browser, list, detail, shell,
    │                   logs, help, namespace/context/container picker)
    ├── terminal.go     Cell-buffer terminal emulator for shell page
    └── app_test.go     Helper function unit tests
```

### Design decisions

- **No tview** — raw tcell gives full control and better rendering performance.
- **Dynamic client first** — List/Delete/Update use the dynamic client for
  resource-generic operations; per-resource DAOs are not needed.
- **Typed client only for special ops** — Logs and Exec use the typed
  `PodInterface`.
- **Describe uses kubectl** — `kubectl describe` gives canonical output.
- **Exec/shell uses kubectl + PTY** — local PTY (creack/pty) bridges
  kubectl stdin/stdout to the embedded terminal emulator.
- **Embedded terminal** — cell-buffer emulator with ANSI escape sequence
  parsing, SGR color support, and dirty-line tracking.
- **Single-goroutine event loop** — `PollEvent` drives the UI; goroutines post
  `renderEvent{}` to trigger screen refresh.

## Architecture

```
main.go
  │
  ├── k8s.NewClient(config)     typed + dynamic + discovery clients
  │
  └── tui.NewApp(client).Run()
        │
        └── eventLoop()
              ├── handleBrowserKey()       resource type browser
              ├── handleListKey()          resource instance list
              ├── handleDetailKey()        YAML / Describe viewer
              ├── handleLogKey()           pod log stream
              ├── handleShellKey()         embedded terminal shell
              ├── handleHelpKey()          help page
              ├── handleNamespaceKey()     namespace picker
              ├── handleContextPickerKey() context picker
              └── handleContainerPickerKey() container picker
```

## Roadmap

- Port forwarding
- Resource creation (currently only edit/delete)
- Custom columns and custom views
- Theme / color scheme support
- Plugin system
- Rollout restart / Scale replicas
- Node management (cordon/drain)

## Contributing

When adding or changing features, please keep both `README.md` and `AGENTS.md`
up to date. If the user-facing documentation grows significantly, extract it
into `docs/` as a standalone manual.

## License

MIT
