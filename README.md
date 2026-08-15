# sshappy-tune

`sshappy-tune` 是面向 Linux 代理节点的宿主机网络检测与受控调优工具。它与代理后端分离运行，不读取数据库、用户密码、`server_key` 或 API Token，也不要求代理容器获得 `NET_ADMIN`、特权模式或 Docker Socket。

当前版本：`0.1.0`

## 安全边界

默认命令只读取系统状态。实际修改必须使用 root，并显式提供 `--confirm`。

版本 0.1 只管理以下两个文件：

```text
/etc/sysctl.d/99-sshappy-tune.conf
/etc/modules-load.d/sshappy-tune-bbr.conf
```

工具会在修改前，把自身管理的 sysctl 原值和上述两个文件的原始内容保存到：

```text
/var/lib/sshappy-tune/snapshots/<snapshot-id>/snapshot.json
```

工具不会：

- 修改代理后端代码或配置。
- 连接 MySQL 或面板 API。
- 打开或挂载 Docker Socket。
- 修改防火墙、路由、DNS、MTU、swap 或用户连接限制。
- 自动启用 HTB 或限制宿主机带宽。
- 在业务运行期间重建活动 qdisc。
- 定时或持续自动修改内核参数。

## 构建

仅支持 Linux。可在任意安装了 Go 1.24 或更高版本的机器上交叉构建：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -o bin/sshappy-tune ./cmd/sshappy-tune
```

ARM64：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -trimpath -o bin/sshappy-tune-arm64 ./cmd/sshappy-tune
```

目标 Linux 宿主机需要提供 `ip`、`tc`、`sysctl` 和 `modprobe`；`inspect-container` 额外需要 `nsenter`。在 Debian/Ubuntu 上分别由 `iproute2`、`procps`、`kmod` 和 `util-linux` 提供。

## 使用流程

检测宿主机，不做修改：

```bash
./sshappy-tune detect
```

检查正在运行容器的网络命名空间。PID 由管理员在工具之外取得，因此工具本身不依赖 Docker：

```bash
PID=$(docker inspect -f '{{.State.Pid}}' ss)
sudo ./sshappy-tune inspect-container --pid "$PID"
```

根据节点带宽、代表性 RTT 和物理内存计算 BDP 与安全缓冲区上限：

```bash
./sshappy-tune recommend --bandwidth 1000 --rtt 150
```

预览完整差异，不写文件、不修改 sysctl：

```bash
./sshappy-tune apply --bandwidth 1000 --rtt 150 --dry-run
```

确认预览后应用：

```bash
sudo ./sshappy-tune apply --bandwidth 1000 --rtt 150 --confirm
```

验证当前状态：

```bash
sudo ./sshappy-tune verify
```

恢复最近一次修改前的原值和文件：

```bash
sudo ./sshappy-tune rollback --confirm
```

回滚会恢复持久化文件和 sysctl 原值，但不会强制卸载已经加载的 `tcp_bbr` 内核模块；卸载一个正在使用的拥塞控制模块并不安全。

也可以指定快照：

```bash
sudo ./sshappy-tune rollback --snapshot 20260815T010203.000000004Z --confirm
```

所有只读命令均支持 `--json`，便于保存检查结果。

## 计算规则

版本 0.1 只支持 `proxy` 角色：

```text
BDP = 带宽(Mbps) × RTT(ms) × 125
目标缓冲区 = 2 × BDP + 2 MiB
代理内存上限 = 物理内存 / 32
最终上限 = min(目标缓冲区, 代理内存上限, 256 MiB)
```

最低缓冲区上限为 4 MiB。`somaxconn` 和 SYN backlog 只在当前值低于 8192 时提高，不会降低宿主机已有的更高值。

## qdisc 行为

工具认为以下状态可用：

- 根 qdisc 为 `fq`。
- 根 qdisc 为 `mq`，且每个硬件发送队列的叶子均为 `fq`。

`net.core.default_qdisc=fq` 会写入持久化配置，但版本 0.1 不会替换活动 qdisc。这样可以避免把正确的 `mq + fq leaves` 误改成单队列，或破坏宿主机已有的复杂整形规则。`verify` 会对不符合要求的活动 qdisc 给出警告。

## 开发检查

```bash
go test ./...
go vet ./...
```

CI 仅在 Linux 上运行，并构建 Linux AMD64 和 ARM64 二进制。

## 致谢

设计参考了 MIT License 项目 [Kylin010/tcpfit](https://github.com/Kylin010/tcpfit)。完整声明见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。
