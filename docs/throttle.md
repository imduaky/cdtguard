# Throttle to 1 Mbit/s instead of shutting down

At 1 Mbit/s a server moves at most ~320 GB/month, in practice far less. You stay
reachable (SSH, panel) and the quota barely grows. We use
[CAKE](https://man7.org/linux/man-pages/man8/tc-cake.8.html): one qdisc that does the shaping, fair queuing
per flow/host and keeps latency low, so SSH stays usable even while a download is capped.

Linux `tc` rules vanish on reboot, and cdtguard runs its limit command only **once per
cycle**, so three hooks are needed:

| When | `cdtguard run` flag | Script call |
|---|---|---|
| limit reached | `-cmd` | `cdtguard-throttle on` |
| server rebooted later in the same cycle | `-restore-cmd` | `cdtguard-throttle on` |
| quota reset (1st, 12:00 Beijing) | `-reset-cmd` | `cdtguard-throttle off` |

## 1. Script `/usr/local/bin/cdtguard-throttle`

```sh
#!/bin/sh
# usage: cdtguard-throttle on|off [iface]   (iface defaults to the default-route NIC)
IF=${2:-$(ip route show default | awk '{print $5; exit}')}
IFB=ifb-cdt
RATE=1mbit

off() {
	tc qdisc del dev "$IF" root 2>/dev/null
	tc qdisc del dev "$IF" ingress 2>/dev/null
	ip link del "$IFB" 2>/dev/null
	return 0
}

case "$1" in
on)
	off
	# egress: CAKE directly on the NIC
	tc qdisc add dev "$IF" root cake bandwidth $RATE || exit 1
	# ingress: redirect to an IFB device and shape there
	modprobe ifb numifbs=0 2>/dev/null
	ip link add "$IFB" type ifb && ip link set "$IFB" up || exit 1
	tc qdisc add dev "$IF" handle ffff: ingress
	tc filter add dev "$IF" parent ffff: matchall action mirred egress redirect dev "$IFB"
	tc qdisc add dev "$IFB" root cake bandwidth $RATE ingress
	;;
off) off ;;
*) echo "usage: $0 on|off [iface]" >&2; exit 1 ;;
esac
```

```bash
sudo chmod +x /usr/local/bin/cdtguard-throttle
sudo cdtguard-throttle on      # try it, e.g. curl a big file
tc -s qdisc show dev eth0      # shows the cake qdisc and its counters
sudo cdtguard-throttle off
```

Requirements: `tc` from iproute2 (Alpine: `apk add iproute2-tc`) and the `sch_cake`,
`ifb`, `act_mirred`, `cls_matchall` kernel modules (Linux ≥ 4.19; stock Debian, Ubuntu and
Alpine kernels have them; some minimal cloud kernels don't — check with `modprobe sch_cake`).

## 2. Run cdtguard with the hooks

Commands contain spaces, so override `ExecStart` instead of using `/etc/default/cdtguard`:

```bash
sudo systemctl edit cdtguard
```
```ini
[Service]
ExecStart=
ExecStart=/usr/bin/cdtguard run -limit 190G \
  -cmd "/usr/local/bin/cdtguard-throttle on" \
  -restore-cmd "/usr/local/bin/cdtguard-throttle on" \
  -reset-cmd "/usr/local/bin/cdtguard-throttle off"
```
```bash
sudo systemctl restart cdtguard
```

Alpine (OpenRC), `/etc/conf.d/cdtguard`:
```sh
command_args="run -limit 190G -cmd '/usr/local/bin/cdtguard-throttle on' -restore-cmd '/usr/local/bin/cdtguard-throttle on' -reset-cmd '/usr/local/bin/cdtguard-throttle off'"
```

## Caveats

- CDT bills `max(rx, tx)`, so both directions are limited. **Egress shaping is exact.**
  CAKE's `ingress` mode drops/marks excess packets so remote TCP senders slow down, but the
  NIC counter has already counted the packets that arrived before being dropped.
- Set `-limit` lower than you would for shutdown (e.g. 190G) so the traffic that still gets
  through while throttled stays within the 200 GB quota.
- cdtguard keeps counting while throttled; the throttle is lifted automatically at the
  next reset by `-reset-cmd`.
