# CDTGuard

Tiny daemon for Aliyun CDT servers (200 GB/month, reset at 12:00 on the 1st, Beijing time).
It counts NIC traffic and runs a command (default `shutdown now`) when the limit is reached.

- Static binary, ~2 MB, no dependencies; the state file is a fixed **63 bytes** (overwritten in place, so it never grows and there is nothing to prune)
- `.deb` (systemd) and `.apk` (Alpine, OpenRC) packages for amd64/arm64
- Bills like Aliyun CDT: `max(rx, tx)` of the NIC counters
- Survives reboots: counters are folded in using the boot time
- Resets itself at the Aliyun reset moment each month

## Commands

```
cdtguard run    [flags]            run the daemon
cdtguard init   [flags] <usage>    set this cycle's real usage, e.g. cdtguard init 120G
cdtguard status [flags]            show the saved state
```

Shared flags: `-state` (default `/var/lib/cdtguard/state.bin`), `-limit` (`195G`, 1024-based units), `-mode` (`max` = max(rx,tx) like Aliyun CDT, or `total` = rx+tx, `tx`, `rx`), `-iface` (run/init; auto when there is exactly one non-`lo` NIC, otherwise required).

`run` flags:

| Flag | Default | |
|---|---|---|
| `-cmd` | `shutdown now` | Run via `sh -c` when the limit is reached |
| `-reset-cmd` | | Run when a new cycle starts after `-cmd` had run (e.g. lift a speed limit) |
| `-restore-cmd` | | Run at startup if `-cmd` already ran this cycle (e.g. re-apply a speed limit after reboot) |
| `-interval` | `1m` | Sampling interval |

## Behaviour

- **First start, no state file**: baseline = current NIC counters (traffic since boot). If the server booted before the current billing cycle, a warning says the counters include older traffic and suggests `cdtguard init`.
- **Already over the limit on first start**: `run` prints the situation and **refuses to start** (exit code 2, systemd will not retry). Look up this cycle's real usage in the Aliyun console, then:
  ```bash
  sudo cdtguard init 120G          # must be below the limit; written straight into the state file
  sudo systemctl start cdtguard
  ```
  `init` also works at any other time to correct the counter (stop the daemon first); it overwrites the state file.
- **State file exists**: if the server rebooted since the last save, the whole current counter is added to the stored total; otherwise only the delta.
- **Fired once per cycle**: "fired" is written to disk *before* the command runs. If you power the server back on from the console after a shutdown, cdtguard logs a notice that the action already ran this month and will **not** run again until the next reset. If the command itself fails, it is retried.
- **Monthly reset**: on the 1st at 12:00 (UTC+8) usage and the fired flag are cleared.

Want a speed limit (CAKE) instead of a shutdown? See [docs/throttle.md](docs/throttle.md).

## Install

```bash
# Debian/Ubuntu: enabled and started automatically
sudo dpkg -i cdtguard_<ver>_amd64.deb
sudoedit /etc/default/cdtguard && sudo systemctl restart cdtguard

# Alpine
sudo apk add --allow-untrusted cdtguard_<ver>_x86_64.apk
sudoedit /etc/conf.d/cdtguard && sudo rc-update add cdtguard && sudo rc-service cdtguard start
```

Check: `cdtguard status`, `journalctl -u cdtguard`.

## Build / release

```bash
go test ./...
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o cdtguard ./cmd/cdtguard
```

Pushing a `v*` tag builds binaries and packages with [nfpm](https://nfpm.goreleaser.com) and attaches them to a GitHub release (`.github/workflows/release.yml`).
