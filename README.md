# CDTGuard

CDTGuard 监控 Linux 网卡流量，在阿里云 CDT 月度额度用尽时执行关机命令。
计费周期于每月 1 日 12:00（北京时间）重置，默认按 `max(rx, tx)` 计量，限额为 `195G`。

## 安装与启动

安装包不会自动启动服务。安装后初始化本周期已用流量，再启用并启动服务：

```bash
sudo dpkg -i cdtguard_<版本>_amd64.deb
sudo cdtguard init 20G --limit 195G -iface eth0 -mode max
sudo systemctl enable --now cdtguard
```

Alpine 使用 apk 和 OpenRC：

```bash
sudo apk add --allow-untrusted cdtguard_<版本>_x86_64.apk
sudo cdtguard init 20G --limit 195G -iface eth0 -mode max
sudo rc-update add cdtguard default
sudo rc-service cdtguard start
```

把 `20G` 换成阿里云控制台显示的本周期用量；若省略该参数，程序会以所选网卡当前累计计数作为初始用量。初始用量必须低于限额。

`-iface`、`-mode` 和 `-limit` 会保存在 `/var/lib/cdtguard/state.bin` 中。只有一张可用网卡时，网卡会自动选择；`lo`、Tailscale、WireGuard 和常见容器、桥接、隧道接口会被忽略。多张可用网卡时，请用 `-iface` 指定，例如 `ens2`。

初始化后，服务会运行 `cdtguard run`。也可以直接运行它：没有 state 文件时会自动选择网卡，并以默认的 `195G` 和 `max` 配置开始；如果初始累计流量已经达到限额，程序会拒绝启动。

查看用量和历史：

```bash
cdtguard status
```

默认达到限额时执行 `shutdown now`，每分钟采样一次。命令行选项和限速等高级示例见：

```bash
cdtguard init -h
cdtguard run -h
cdtguard status -h
```

## 查看日志

```bash
sudo journalctl -u cdtguard
```

Alpine 日志默认写入 `/var/log/cdtguard.log`。
