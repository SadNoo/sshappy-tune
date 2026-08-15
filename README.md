# sshappy-tune

`sshappy-tune` 是面向 Linux 代理节点的宿主机网络检测与受控调优工具。它与代理后端分离运行，不读取数据库、用户密码、`server_key` 或 API Token，也不要求代理容器获得 `NET_ADMIN`、特权模式或 Docker Socket。

当前版本：`0.2.0`

## 安全边界

默认命令只读取系统状态。实际修改必须使用 root，并显式提供 `--confirm`。

版本 0.2 管理以下文件：

```text
/etc/sysctl.d/99-sshappy-tune.conf
/etc/modules-load.d/sshappy-tune-bbr.conf
/etc/sshappy-tune/profile.json
/etc/systemd/system/sshappy-tune-apply.service
/etc/systemd/system/sshappy-tune-verify.service
/etc/systemd/system/sshappy-tune-verify.timer
```

每次应用 TCP 参数前，工具会把自身管理的 sysctl 原值，以及 sysctl/BBR 两个核心文件的原始内容保存到：

```text
/var/lib/sshappy-tune/snapshots/<snapshot-id>/snapshot.json
```

profile 和三个 systemd unit 使用独立的事务式安装：如果安装中途失败，工具会恢复安装前的文件状态。它们不属于上述 sysctl 回滚快照。

工具不会：

- 修改代理后端代码或配置。
- 连接 MySQL 或面板 API。
- 打开或挂载 Docker Socket。
- 修改防火墙、路由、DNS、MTU、swap 或用户连接限制。
- 自动启用 HTB 或限制宿主机带宽。
- 在业务运行期间重建活动 qdisc。
- 根据短时网络波动持续修改内核参数。

定时器每6小时执行一次只读 `verify`。开机服务只在已确认的 profile、sysctl或自有文件发生漂移时校准；没有漂移就不会写入参数或创建快照。

## 一键安装并启动

一键脚本仅支持带 systemd 的 Linux AMD64/ARM64。它固定下载 `v0.2.0` release，核对 SHA-256 后安装二进制，先输出完整 dry-run，再执行首次校准并启动自动维护：

```bash
curl -fsSL https://raw.githubusercontent.com/SadNoo/sshappy-tune/v0.2.0/install.sh \
  | sudo bash -s -- \
      --bandwidth 1000 \
      --rtt 150 \
      --confirm
```

其中：

- `--bandwidth` 是节点预期可用带宽，单位 Mbps。
- `--rtt` 是代表性用户线路 RTT，单位毫秒。
- `--confirm` 表示允许首次应用持久化 TCP 配置并安装 systemd 服务。

脚本不会安装系统软件包。目标机器需要 `curl`、`sha256sum`、`install`、`ip`、`tc`、`sysctl`、`modprobe` 和 `systemctl`。

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

已经把二进制安装到 `/usr/local/sbin/sshappy-tune` 时，也可以手动安装自动维护：

```bash
sudo /usr/local/sbin/sshappy-tune service install \
  --bandwidth 1000 \
  --rtt 150 \
  --confirm
```

查看自动维护状态：

```bash
/usr/local/sbin/sshappy-tune service status
systemctl list-timers sshappy-tune-verify.timer
```

手动使用已保存 profile 进行幂等校准：

```bash
sudo /usr/local/sbin/sshappy-tune reconcile --confirm
```

只有检测到 sysctl 或自有持久化文件漂移时，`reconcile` 才会应用并创建回滚快照。

移除自动维护服务和 profile：

```bash
sudo /usr/local/sbin/sshappy-tune service uninstall --confirm
```

卸载自动维护不会自动回滚已经生效的 sysctl。需要恢复参数时，应先执行 `rollback --confirm`，再卸载服务。

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

版本 0.2 只支持 `proxy` 角色：

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

`net.core.default_qdisc=fq` 会写入持久化配置，但工具不会替换活动 qdisc。这样可以避免把正确的 `mq + fq leaves` 误改成单队列，或破坏宿主机已有的复杂整形规则。`verify` 会对不符合要求的活动 qdisc 给出警告。

## 开发检查

```bash
go test ./...
go vet ./...
```

CI 仅在 Linux 上运行，并构建 Linux AMD64 和 ARM64 二进制。

## 致谢

设计参考了 MIT License 项目 [Kylin010/tcpfit](https://github.com/Kylin010/tcpfit)。完整声明见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。
