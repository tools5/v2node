# v2node
A v2board backend base on moddified xray-core.
一个基于修改版xray内核的V2board节点服务端。

**注意： 本项目需要搭配[修改版V2board](https://github.com/wyx2685/v2board)**

## 软件安装

### 一键安装

```
wget -N https://raw.githubusercontent.com/tools5/v2node/main/script/install.sh && bash install.sh
```

## 构建
``` bash
GOEXPERIMENT=jsonv2 go build -v -o build_assets/v2nb -trimpath -ldflags "-X 'github.com/wyx2685/v2node/cmd.version=$version' -s -w -buildid="
```

## AnyTLS / VLESS 共用入口

现有 AnyTLS TCP + TLS 节点可以在同一个域名、端口和 NodeID 上提供两种协议。升级节点后，在原 `Nodes` 条目设置 `"AnyTLSVLESS": true` 即可启用，无需新增 VLESS 节点；默认保持原有 AnyTLS 行为。

需要配套面板的订阅映射，并且只向用户开放已升级、实测的节点。共享子线路继续使用自己的出口和原账号计费。此入口支持普通 VLESS TCP + TLS，不支持 Vision、Reality 或 WebSocket。完整配置、订阅更新和回滚说明见 [docs/dual-tls.md](docs/dual-tls.md)。

## 实时连接

配套的 V2board 管理端在用户详情中提供「实时连接」，支持按节点查看当前连接的来源、目标、出站、上传/下载流量与持续时间，以及断开单条连接或该用户当前连接。自动刷新间隔为 5 秒；查询需要节点响应，页面会区分等待、离线和过期数据。

同时更新节点二进制和带有实时连接接口/界面的面板后即可使用。节点沿用 `ApiHost`、`ApiKey`、`NodeID` 主动向面板领取请求，不需要新增监听端口。面板的 `ApiHost` 地址应直接使用正确的 HTTP/HTTPS 地址，此接口不跟随重定向。旧面板不支持接口时每 5 分钟再探测，节点代理服务不受影响。

该通道默认启用，可以在单个 `Nodes` 配置项中设置 `"RealtimeConnections": false` 停止查询/断开请求的轮询。节点按需提交快照；空闲轮询间隔为 5 秒，查看期间约 2 秒。连接详情仅供管理员查询，节点内部用户 UUID 不会出现在响应中。

注意：列表显示 TCP 流和 UDP 逻辑会话，不等同于底层复用/QUIC 传输连接。断开不会禁用账号，客户端可能立即重连。目标域名来自请求或嗅探，无法获取时显示 IP 和端口；不收集网页内容或完整 URL。内核 splice 加速连接的流量可能在复制结束后才计入，实时数字可能暂时滞后。

每个进程最多保留 10,000 条活动连接记录，每次节点响应最多返回 1,000 条。达到上限时页面会提示数据不完整，查询和断开不会影响其余连接的转发。面板快照缓存有效期为 30 秒，不写入历史连接数据库；关闭请求有效期为 20 秒并绑定节点进程，过期或重试不会误断新进程的连接。

开发验证：

```bash
GOEXPERIMENT=jsonv2 go test ./...
GOEXPERIMENT=jsonv2 CGO_ENABLED=1 go test -race ./common/connections ./common/connectioncontrol ./api/v2board ./core/app/dispatcher ./core
```

## 进程隐藏（Stealth）

节点进程默认在 ps / 任务管理器里一眼可见：进程名 v2nb、命令行带 server --config、环境变量可读。
新增的 common/stealth 提供分层隐藏：

| 层 | 能力 | 需要特权 |
| --- | --- | --- |
| L1 用户态 | 进程名（comm）、argv 清零写别名、environ 清零、二进制 unlink；Windows 另加 PEB 命令行改写 | 无 |
| L2 内核态 | Linux kprobe 过滤 /proc 目录项（ps/top/ls /proc 全看不到）；Windows DKOM 摘活动进程链（任务管理器/tasklist/WMI 全看不到） | root / 驱动加载权限 |

**一键安装即自动开启隐藏**：`install.sh` 安装完成后会自动写入 Stealth 配置并尝试编译内核模块，
成功则直接启用内核级隐藏（彻底从 ps 消失），失败则降级为纯用户态伪装，不影响节点运行。

管理脚本：`v2nb stealth status|enable|install|verify|disable`，或菜单第 15 项。

手动配置方式（config.json）：

~~~
"Stealth": {
  "Enable": true,
  "Alias": "kworker/1:1",
  "Kernel": true,
  "KernelModule": "/etc/v2nb/v2hide.ko",
  "HideEnv": true,
  "HideModule": true,
  "Debug": true
}
~~~

- 内核组件源码：tools/linux-hide（v2hide.ko）、tools/win-hide（v2hide.sys）
- 验证脚本：sudo ./script/stealth-verify.sh
- 设计与风险说明：docs/stealth-report.md

注意：内核对自身视角默认放行（保证 /proc/self 可用），对其他进程不可见；
容器内无 CAP_SYS_MODULE 时 L2 会自动降级为纯用户态伪装并打日志。

### 各发行版支持

| 发行版 | 节点本体 | 内核模块（L2） | 说明 |
| --- | --- | --- | --- |
| Debian / Ubuntu | ✅ | ✅ | `apt install linux-headers-$(uname -r) build-essential` |
| CentOS / RHEL / Rocky / Alma 8+ | ✅ | ✅ | `yum install kernel-devel-$(uname -r) gcc make` |
| CentOS 7 | ✅ | ✅ | 内核 3.10，模块内置兼容层（simple_strtol / 自实现 strim / strscpy） |
| Alpine | ✅ | ⚠️ | 需 `apk add linux-headers`，musl 环境编译内核模块易失败，建议仅用 L1 |
| Arch | ✅ | ✅ | `pacman -S linux-headers base-devel` |
| 容器（Docker/LXC） | ✅ | ❌ | 无 CAP_SYS_MODULE，自动降级为 L1 |

CentOS 装内核模块的完整步骤：

~~~
yum install -y kernel-devel-$(uname -r) kernel-headers gcc make
# 若 yum 提示找不到 kernel-devel-$(uname -r)：先 yum update kernel 然后 reboot，再执行上面的命令
v2nb stealth install
v2nb stealth verify
~~~

## Stars 增长记录

[![Stargazers over time](https://starchart.cc/wyx2685/v2node.svg?variant=adaptive)](https://starchart.cc/wyx2685/v2node)
