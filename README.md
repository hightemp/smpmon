# smpmon

[![CI](https://github.com/hightemp/smpmon/actions/workflows/ci.yml/badge.svg)](https://github.com/hightemp/smpmon/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

A terminal resource monitor in Go, inspired by [btop++](https://github.com/aristocratos/btop), built around **configurable dashboards** and **graphs**.

> **Status:** early development. The TUI isn't implemented yet.

## Planned features

- CPU, memory, swap, load, disks, disk I/O, network and processes, plus GPU, sensors, battery and Docker
- Linux, macOS and Windows from one static binary (no cgo)
- Dashboards described as a YAML layout tree of widgets, with built-in presets (`btop`, `minimal`, `procs`, `net`, `server`, `gpu`) and an in-TUI layout editor
- Graph rendering in braille, block or ASCII mode, with multiple series, mirrored graphs and auto or log scales
- Generic `graph`, `gauge`, `stat` and `table` widgets that work with any metric selector, such as `cpu.core.*.usage`
- Themes, mouse support and rebindable keys
- On-disk metric history, alerts, Prometheus and JSON export
- Remote monitoring through `smpmon agent` (gRPC over TLS with token auth)

## Install

From source (requires Go 1.26+):

```sh
go install github.com/hightemp/smpmon/cmd/smpmon@latest
```

Prebuilt binaries and deb/rpm packages are published on the [Releases](https://github.com/hightemp/smpmon/releases) page.

## Usage

```sh
smpmon                 # start the TUI
smpmon -d procs        # open a specific dashboard
smpmon -c config.yaml  # use a specific config file
smpmon version
```

## Development

```sh
make build      # build bin/smpmon
make test       # run tests
make lint       # run golangci-lint
make build-all  # cross-compile for all platforms
make help       # list all targets
```

### Releasing

The release version lives in the `VERSION` file.

1. Bump `VERSION`, for example `0.2.0`.
2. Run `make release`. It checks that you're on `main` with a clean tree, commits `VERSION`, creates the `v<VERSION>` tag and pushes the branch and the tag.
3. The `Release` GitHub workflow runs GoReleaser and publishes binaries, checksums and deb/rpm packages.

## License

[MIT](LICENSE)
