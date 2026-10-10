# smpmon

[![CI](https://github.com/hightemp/smpmon/actions/workflows/ci.yml/badge.svg)](https://github.com/hightemp/smpmon/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

A terminal resource monitor in Go, inspired by [btop++](https://github.com/aristocratos/btop), built around **configurable dashboards** and **graphs**.

> **Status:** early development. Linux is the primary platform; macOS and Windows support is new and tested only in CI.

![smpmon showing the Overview dashboard](docs/screenshot.svg)

## Features

- CPU (total and per core, frequency, temperature, load), memory and swap (including the ZFS ARC), filesystems, disk I/O, network and processes
- GPUs (NVIDIA through `nvidia-smi`, AMD and Intel through sysfs on Linux), temperature and fan sensors, batteries, and Docker containers
- Linux, macOS and Windows
- Dashboards described as a YAML layout tree of widgets, switchable with `1`–`9`, with size-dependent variants for narrow terminals
- Built-in dashboards: `btop`, `minimal`, `procs`, `net`, `server`, `hardware`, `containers`. Widgets whose data isn't available on the machine, such as a battery on a desktop, are hidden automatically
- Graphs in braille, block or ASCII mode, with gradients, mirrored upload/download, and auto or log scale
- Generic `graph`, `gauge`, `stat` and `table` widgets that show any metric by selector, such as `cpu.core.*.usage` or `net.*.*_bps`
- A process table with configurable columns, sorting, filtering, a tree view, a details panel, signals and renice
- Mouse support, built-in themes, and config hot reload
- A single static binary with no cgo

Planned: an in-TUI layout editor, theme files, rebindable keys, metric history, alerts, Prometheus export and remote agents.

## Install

From source (requires Go 1.26+):

```sh
go install github.com/hightemp/smpmon/cmd/smpmon@latest
```

Prebuilt binaries and deb/rpm packages are published on the [Releases](https://github.com/hightemp/smpmon/releases) page.

### Platform notes

| | Linux | macOS | Windows |
|---|---|---|---|
| CPU, memory, disks, network, processes | `/proc`, `/sys` | sysctl, libproc | Win32, NT API |
| Temperatures | hwmon | SMC | WMI (often needs administrator rights) |
| GPU | NVIDIA, AMD, Intel | — | NVIDIA |
| Battery | sysfs | `pmset` | `GetSystemPowerStatus` |
| Docker | socket | Docker Desktop, Colima | `DOCKER_HOST=tcp://…` only |
| Process signals | all | all | terminate only |

## Usage

```sh
smpmon                    # start the TUI
smpmon -d procs           # open a specific dashboard
smpmon -c config.yaml     # use a specific config file
smpmon snapshot           # print all current metrics once
smpmon snapshot -s 'net.total.*' --json
smpmon config default     # print the default config with comments
smpmon config validate    # check your config
smpmon version
```

### Keys

| Key | Action |
|---|---|
| `1`–`9` | switch dashboard |
| `tab` / `shift+tab` | focus next / previous widget |
| `z`, `esc` | zoom the focused widget, unzoom |
| `+` / `-` | change the update interval |
| `g` | cycle graph mode (braille / block / ascii) |
| `p` | pause the display |
| `?` | help |
| `q`, `ctrl+c` | quit |

In the process widget:

| Key | Action |
|---|---|
| `↑` `↓` `pgup` `pgdn` `home` `end` | select a process |
| `←` `→`, `r` | change the sort column, reverse the order |
| `f` or `/` | filter by name, command, user or pid |
| `t`, `space` | tree view, collapse or expand a branch |
| `enter` | details panel |
| `k` / `K` / `s` | send SIGTERM / SIGKILL / pick a signal |
| `i` | renice |
| `c` | show CPU per core or as a share of the whole machine |

In the network widget, `b` and `n` switch the interface. In the Docker widget, `←` `→` change the sort order and `a` shows stopped containers. Mouse: click a widget or tab to select it, click a column header to sort, use the wheel to scroll.

## Configuration

The config lives in `~/.config/smpmon/config.yaml` (`smpmon config path` prints the exact location). It only needs to contain what you change: it is merged over the built-in defaults, which `smpmon config default` prints with comments. Edits are applied while smpmon is running.

```yaml
general:
  interval: 1s           # minimum 100ms
  graph_mode: braille    # braille | block | ascii
  theme: default         # default | nord | gruvbox | dracula | monochrome
  border: rounded        # rounded | single | double | thick | none
  dashboards: [btop, mine, procs]
  default_dashboard: mine

collectors:
  procs: {interval: 2s}

dashboards:
  mine:
    title: My dashboard
    layout:
      col:
        - row:
            - widget: cpu
              weight: 2
            - widget: gauge
              title: Root FS
              options: {metric: "disk.fs./.used_percent", gradient: disk}
          size: 40%
        - row:
            - widget: graph
              title: Cores
              options:
                metrics: ["cpu.core.*.usage"]
                window: 5m
                mode: block
            - widget: proc
              weight: 2
              options:
                columns: [pid, user, cpu, mem, rss, name, cmd]
                sort: {by: mem}
    variants:
      - when: {max_width: 99}   # narrow terminals
        layout:
          col:
            - widget: cpu
            - widget: proc
```

A layout node is one of `row` (children side by side), `col` (children stacked) or `widget`. A node's share of its parent is set with `size: 10` (cells), `size: 30%` or `weight: 2` (share of the remaining space, default 1), optionally bounded by `min` and `max`.

### Widgets

| Widget | Options |
|---|---|
| `cpu` | `cores` (bool), `mode` |
| `mem` | `graph` (bool), `mode` |
| `disks` | `mounts` (list), `io` (bool) |
| `net` | `interface` (`auto` or a name), `scale` (`auto` or `log`), `mode` |
| `proc` | `columns`, `sort: {by, desc}`, `tree`, `filter`, `per_core` |
| `graph` | `metrics` (selectors), `window`, `mode`, `style` (`area` or `line`), `scale`, `min`, `max`, `gradient`, `legend`, `limit` |
| `gauge` | `metric`, `max`, `gradient` |
| `stat` | `metric`, `sparkline`, `gradient` |
| `table` | `metrics` (selectors) |
| `text` | `text` (with `{hostname}` `{os}` `{kernel}` `{uptime}` `{time}` `{date}`), `align` |
| `gpu` | `index`, `mode` |
| `sensors` | `filter`, `fans` (bool) |
| `battery` | — |
| `docker` | `all` (include stopped), `sort` (`cpu`, `mem`, `net` or `name`) |

Process columns: `pid`, `ppid`, `name`, `cmd`, `user`, `state`, `threads`, `nice`, `cpu`, `mem`, `rss`, `vms`, `read`, `write`, `time`, `start`.

### Metrics

Metric keys are dot-separated. Selectors support `*` (one segment), `**` (any number of segments) and globs inside a segment (`*_bps`, `eth?`). Run `smpmon snapshot` to list every metric on your machine.

| Keys | Unit |
|---|---|
| `cpu.total.{usage,user,system,iowait,steal}`, `cpu.core.<n>.usage` | % |
| `cpu.freq`, `cpu.core.<n>.freq` | Hz |
| `load.{1,5,15}`, `system.uptime` | — , s |
| `mem.{total,used,available,free,cached,buffers,zfs_arc}`, `swap.{total,used,free}` | bytes |
| `mem.used_percent`, `swap.used_percent` | % |
| `disk.fs.<mount>.{total,used,free,used_percent}` | bytes, % |
| `disk.io.<dev>.{read_bps,write_bps,read_iops,write_iops,util}` | bytes/s, 1/s, % |
| `net.<if>.{rx_bps,tx_bps,rx_pps,tx_pps,rx_errs,tx_errs,rx_total,tx_total}` | bytes/s, 1/s, bytes |
| `net.total.*` | sums over physical NICs |
| `proc.{count,threads,running}`, `proc.<pid>.{cpu,mem}` | count, % |
| `cpu.temp`, `sensors.<chip>.<label>.temp`, `sensors.<chip>.<label>.fan` | °C, RPM |
| `gpu.<n>.{util,mem_util,mem_used_percent,fan}`, `gpu.<n>.{mem_used,mem_total}`, `gpu.<n>.temp`, `gpu.<n>.{power,power_limit}`, `gpu.<n>.{clock,mem_clock}` | %, bytes, °C, W, Hz |
| `battery.{percent,power,time_left}` | %, W, s |
| `docker.<name>.{cpu,mem_percent}`, `docker.<name>.{mem_used,mem_limit}`, `docker.<name>.{net_rx_bps,net_tx_bps,blk_read_bps,blk_write_bps}` | %, bytes, bytes/s |

Dots inside a segment are escaped with a backslash, for example `disk.fs./mnt/data\.v2.used`.

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
