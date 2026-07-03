# sterm — Simplified Kubernetes Terminal TUI

sterm is a lightweight, keyboard-driven Kubernetes management TUI built with
`tcell/v2` and `k8s.io/client-go`. It provides a terminal interface for browsing
cluster resources, inspecting YAML/descriptions, streaming logs, editing
resources, and exec'ing into containers — all without leaving the terminal.

## Features

| Page | Keys | Action |
|---|---|---|
| **Browser** | Arrow keys / PgUp / PgDn / Home / End | Navigate resource types (3 categories: common / crd / other) |
| | Enter | Enter resource instance list |
| | `n` | Open namespace picker |
| | ESC / Ctrl+C | Quit |
| **Namespace** | Arrow keys / PgUp / PgDn / Home / End | Navigate namespaces |
| | Enter | Select namespace (`all` = all namespaces) |
| | `/` | Live filter |
| | ESC | Back to previous page |
| **List** | Arrow keys / PgUp / PgDn / Home / End | Navigate rows |
| | Enter | View YAML / Describe |
| | `/` | Live filter (matches any column) |
| | `x` | Delete (confirm y/N) |
| | `l` | Pod log stream (auto-follow) |
| | `s` | Enter container shell |
| | `n` | Switch namespace |
| **Detail** | Arrow keys / PgUp / PgDn / Home / End | Scroll |
| | `d` | Toggle YAML / Describe |
| | `e` | Edit YAML (external $EDITOR) |
| | `s` | Enter container shell |
| **Logs** | Up / Down | Scroll (auto-pause follow) |
| | End | Resume auto-follow |
| | ESC / Ctrl+C | Back to list |

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

- Go 1.26.4+
- A valid kubeconfig (or in-cluster config)
- `kubectl` on `$PATH` (for exec/shell feature)

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
pkg/
├── k8s/
│   ├── client.go     Typed + dynamic + discovery client wrapper
│   ├── resource.go   Discover() lists all cluster GVRs
│   ├── lister.go     List/Get/Delete/Update/ToYAML/Namespaces
│   ├── describe.go   Describe() formats resource summary
│   └── logs.go       StreamLogs() + PodContainers()
└── tui/
    └── app.go        Multi-page TUI (browser / namespace / list / detail / logs)
                      renderEvent{} drives async rendering via PostEvent
```

### Design decisions

- **No tview** — raw tcell gives full control and better rendering performance.
- **Dynamic client first** — List/Delete/Update use the dynamic client for
  resource-generic operations; per-resource DAOs are not needed.
- **Typed client only for special ops** — Logs and Exec use the typed
  `PodInterface`.
- **Describe is not kubectl describe** — extracts key fields from unstructured
  objects without shelling out to kubectl.
- **Edit uses external editor** — suspends TUI, launches `$EDITOR` (default vi),
  reads back the file, and calls Update.
- **Exec shells out to kubectl** — simplest reliable TTY handling.
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
              ├── handleBrowserKey()    resource type browser
              ├── handleNamespaceKey()  namespace picker
              ├── handleListKey()       resource instance list
              ├── handleDetailKey()     YAML / Describe viewer
              └── handleLogKey()        pod log stream
```

## Roadmap

- Port forwarding
- Resource creation (currently only edit/delete)
- Multi-container selection (logs/shell default to first container)
- Custom columns and custom views
- Theme / color scheme support
- Better error reporting (currently silently ignored)
- Separate user manual (`docs/` directory)

## Contributing

When adding or changing features, please keep both `README.md` and `AGENTS.md`
up to date. If the user-facing documentation grows significantly, extract it
into `docs/` as a standalone manual.

## License

MIT
