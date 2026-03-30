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
- optional 文件的最小 hashfield 选择下载
- `udp://`、`http(s)://`、`zero://` tracker 接入
- 启动时从 `https://raw.githubusercontent.com/XIU2/TrackersListCollection/refs/heads/master/all.txt` 拉取 tracker 列表并合并
- 本地站点目录与 `content.json` 索引
- 缺文件时自动回源下载的 HTTP 静态服务
- ZeroFrame 最小兼容层
- 站点私钥存储、`fileWrite`、`siteSign`、`sitePublish`
- `site new` / `site clone`
- 站点签名时自动重建 `content.json` 文件哈希并生成 Bitcoin 消息签名

当前仍未实现：

- Tor
- 完整的 ZeroFrame / WebSocket API
- 用户证书体系与用户内容签名
- 增量 diff 发布
- 完整的 include / user_contents 规则兼容

## 运行

可以直接依赖 tracker 找 peer，也可以额外指定一个 Python ZeroNet bootstrap peer。

然后运行：

```bash
go run ./cmd/go-zeronet \
  --peer 127.0.0.1:15441 \
  --ui-addr 127.0.0.1:43110 \
  --data-dir ./data
```

如果不想依赖本地 Python 节点，也可以只用 tracker：

```bash
go run ./cmd/go-zeronet \
  --peer '' \
  --ui-addr 127.0.0.1:43110 \
  --data-dir ./data
```

可选参数：

- `--trackers`：手动追加/覆盖 tracker，多个用逗号分隔
- `--disable-udp`：禁用 UDP tracker
- `--working-shared-trackers-limit`：共享 zero tracker 上限

打开：

- `http://127.0.0.1:43110/` 查看说明
- `http://127.0.0.1:43110/<site-address>` 打开站点

## 站点创建

新建一个最小站点：

```bash
go run ./cmd/go-zeronet site new \
  --data-dir ./data \
  --title "My ZeroNet Site" \
  --description "Created by go-zeronet"
```

克隆一个已经同步到本地的站点：

```bash
go run ./cmd/go-zeronet site clone \
  --data-dir ./data \
  --source 1Bm8RDrnitgbh7Nbsbo6T9j5VDLWTGaar4
```

命令会输出：

- 新站点地址
- 对应私钥（WIF）

私钥会保存到 `data/sites.json`，HTTP UI 会把这些站点识别为“own site”。

## 签名与发布

当前发布链路：

1. 前端通过 `fileWrite` 写文件
2. `siteSign` 重新扫描目录、更新 `content.json` 的 `files` / `files_optional`
3. 使用 Bitcoin Signed Message 双 SHA256 格式签名
4. `sitePublish` 通过 ZeroNet `update` 命令把 `content.json` 推送到已知 peer

当前限制：

- 只完成站点所有者私钥签名
- 还没有实现完整证书系统，所以用户评论/投票这类 user content 发布还不完整
- 发布依赖至少一个可连接 peer；如果 `127.0.0.1:15441` 没监听，发布会失败

## 目录

- `docs/rewrite-plan.md`: Python 版分析与重写计划
- `cmd/go-zeronet`: 程序入口
- `internal/config`: 配置
- `internal/zeronet/protocol`: 协议与 msgpack
- `internal/zeronet/conn`: P2P 连接客户端
- `internal/zeronet/site`: 站点存储与拉取逻辑
- `internal/httpui`: 本地 HTTP 服务
