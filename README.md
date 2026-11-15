# CDTGuard

Tiny daemon for Aliyun CDT servers (200 GB/month, reset at 12:00 on the 1st, Beijing time).
It counts NIC traffic and runs a command (default `shutdown now`) when the limit is reached.

- Static binary, ~2 MB, no dependencies; the state file is exactly **one filesystem block** (usually 4 KiB, replaced atomically, so it never grows and there is nothing to prune)
- Keeps traffic history in that block: per day for the current cycle, per hour for the last 48 hours, and the last 12 cycles; `cdtguard status` shows all of it
- Logs the current usage every time it grows by another 1% of the limit
- `.deb` (systemd) and `.apk` (Alpine, OpenRC) packages for amd64/arm64
- Bills like Aliyun CDT: `max(rx, tx)` of the NIC counters
- Survives reboots: counters are folded in using the boot time
- Resets itself at the Aliyun reset moment each month

## Commands

```
cdtguard run    [flags]            run the daemon
cdtguard init   [flags] <usage>    set this cycle's real usage, e.g. cdtguard init 120G
cdtguard status [flags]            show the saved state and traffic history
```

Shared flags: `-state` (default `/var/lib/cdtguard/state.bin`), `-limit` (`195G`, 1024-based units), `-mode` (`max` = max(rx,tx) like Aliyun CDT, or `total` = rx+tx, `tx`, `rx`), `-iface` (run/init; auto when there is exactly one non-`lo` NIC, otherwise required).

`run` flags:

| Flag | Default | |
|---|---|---|
| `-cmd` | `shutdown now` | Run via `sh -c` when the limit is reached |
| `-reset-cmd` | | Run when a new cycle starts after `-cmd` had run (e.g. lift a speed limit) |
| `-restore-cmd` | | Run at startup if `-cmd` already ran this cycle (e.g. re-apply a speed limit after reboot) |
| `-interval` | `1m` | Sampling interval |

`-reset-cmd` and `-restore-cmd` are useful for reversible actions such as
traffic shaping: `-cmd` applies the limit action once, `-restore-cmd` reapplies
it after a reboot in the same billing cycle, and `-reset-cmd` removes it when
the next billing cycle starts. For a one-time test marker, `-cmd` is enough.

To verify threshold handling without shutting down the machine, run with a low
limit and make `-cmd` write a file. Initialize to zero first because Linux NIC
counters include traffic from before the daemon starts:

```bash
sudo cdtguard init -iface eth0 -mode rx -limit 1M -state /tmp/cdtguard-test.state 0
sudo cdtguard run -iface eth0 -mode rx -limit 1M -interval 1s \
  -state /tmp/cdtguard-test.state \
  -cmd 'printf threshold-reached > /tmp/cdtguard-limit-reached'
```

Generate more than 1 MiB of received traffic, then check
`/tmp/cdtguard-limit-reached`. Replace `eth0` with the interface you want to
monitor. The test state and marker paths can be anywhere writable by the
process.

## Behaviour

- **First start, no state file**: baseline = current NIC counters (traffic since boot). If the server booted before the current billing cycle, a warning says the counters include older traffic and suggests `cdtguard init`.
- **Already over the limit on first start**: `run` prints the situation and **refuses to start** (exit code 2, systemd will not retry). Look up this cycle's real usage in the Aliyun console, then:
  ```bash
  sudo cdtguard init 120G          # must be below the limit; written straight into the state file
  sudo systemctl start cdtguard
  ```
  `init` also works at any other time to correct the counter (stop the daemon first); it overwrites the state file.
- **State file exists**: if the server rebooted since the last save, the whole current counter is added to the stored total; otherwise only the delta.
- **Fired once per cycle**: "fired" is written to disk *before* the command runs. If you power the server back on from the console after a shutdown, cdtguard logs a notice that the action already ran this month and will **not** run again until the next reset. If the command exits with an error it is retried on the next sample; if it is killed by a signal (systemd stopping the service because the system is going down) it counts as executed.
- **Traffic history**: each sample's traffic is booked to the current Beijing day and hour, so traffic while the daemon was stopped (or before a reboot) lands in the hour it resumes. Usage that predates per-day tracking (NIC counters at first start, `cdtguard init`) is shown as `untracked`. `init` keeps the existing history.
- **Monthly reset**: on the 1st at 12:00 (UTC+8) the cycle is archived into the history, then usage and the fired flag are cleared. Traffic from the last sample before the reset is counted in the new month (errs on the safe side). The cycle only moves forward, so a clock stepped backwards never resets it.

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
make help        # list targets
make ci          # gofmt check, vet, race tests, cross-build (what CI runs)
make tools       # install nfpm (needed for packages)
make packages    # dist/*.deb and dist/*.apk for amd64 + arm64
make release VERSION=1.2.3   # ci + packages + SHA256SUMS
make clean
```

Pushing a `v*` tag runs `make release` in GitHub Actions and attaches the files in `dist/` to a release (`.github/workflows/release.yml`).
