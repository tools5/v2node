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

## Stars 增长记录

[![Stargazers over time](https://starchart.cc/wyx2685/v2node.svg?variant=adaptive)](https://starchart.cc/wyx2685/v2node)
