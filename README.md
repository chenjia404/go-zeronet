# go-zeronet

Go 版 ZeroNet 重写项目。

当前目标不是一次性完全复刻 Python ZeroNet，而是先完成一个可演进的最小互通版本：

1. 连接 Python ZeroNet 节点。
2. 下载 `content.json` 和静态文件。
3. 在本地 HTTP 服务中打开旧站点。
4. 后续逐步补齐 tracker、ZeroFrame、发布与签名等能力。

## 当前状态

已实现第一阶段骨架：

- ZeroNet `v2` 握手客户端
- `ping`
- `getFile` 分块下载
- `pex` 和最小多 peer
- `listModified` 与嵌套 `content.json` 刷新
- 本地站点目录与 `content.json` 索引
- 缺文件时自动回源下载的 HTTP 静态服务

当前仍未实现：

- Tor
- tracker 协议
- ZeroFrame / WebSocket API
- 发布、签名、增量更新
- 完整的 include / user_contents 规则兼容

## 运行

先启动一个 Python ZeroNet 节点，例如本机 `127.0.0.1:15441`。

然后运行：

```bash
go run ./cmd/go-zeronet \
  --peer 127.0.0.1:15441 \
  --ui-addr 127.0.0.1:43110 \
  --data-dir ./data
```

打开：

- `http://127.0.0.1:43110/` 查看说明
- `http://127.0.0.1:43110/<site-address>` 打开站点

## 目录

- `docs/rewrite-plan.md`: Python 版分析与重写计划
- `cmd/go-zeronet`: 程序入口
- `internal/config`: 配置
- `internal/zeronet/protocol`: 协议与 msgpack
- `internal/zeronet/conn`: P2P 连接客户端
- `internal/zeronet/site`: 站点存储与拉取逻辑
- `internal/httpui`: 本地 HTTP 服务
