# ZeroNet Go 重写计划

## 目标

重写一个不依赖 Tor 的 Go 版 ZeroNet，要求满足以下最小目标：

1. 能连接 Python 版 ZeroNet 节点。
2. 能读取并复用旧的 ZeroNet 站点目录结构与 `content.json` 元数据。
3. 能通过本地 HTTP 服务打开历史 ZeroNet 站点。
4. 第一阶段优先保证互通和静态站点可访问，再逐步补齐发布、更新、ZeroFrame、数据库、tracker 等能力。

## Python 版代码地图

### 启动链路

- `zeronet.py`
  - 负责设置工作目录、装载 `src/`，再调用 `main.start()`。
- `src/main.py`
  - 解析配置。
  - 启动 `FileServer` 和 `UiServer`。
  - 挂载站点命令，如 `siteDownload`、`siteNeedFile`、`sitePublish`。
- `src/Config.py`
  - 统一定义运行参数、目录结构、tracker 列表、端口、日志等。

### P2P 网络层

- `src/Connection/ConnectionServer.py`
  - 管理入站/出站连接、连接池、握手、端口状态。
- `src/Connection/Connection.py`
  - TCP 连接对象。
  - 发送 `handshake`。
  - 使用 msgpack 编解码消息。
  - 处理 `response`、流式下载和可选 TLS。
- `src/File/FileServer.py`
  - 基于连接层提供文件协议服务。
- `src/File/FileRequest.py`
  - 协议命令入口。
  - 当前最关键命令：`handshake`、`ping`、`getFile`、`streamFile`、`pex`、`listModified`、`getHashfield`、`update`。

### 站点与存储层

- `src/Site/Site.py`
  - 站点核心对象。
  - 管理 peers、下载任务、站点状态、文件请求。
- `src/Site/SiteManager.py`
  - 管理 `data/sites.json` 和站点实例缓存。
- `src/Site/SiteStorage.py`
  - 文件系统访问、SQLite 缓存、站点目录操作。
- `src/Content/ContentManager.py`
  - 解析 `content.json`。
  - 校验签名和文件 sha512。
  - 计算 changed/deleted/include/user_contents。

### UI 层

- `src/Ui/UiServer.py`
  - 本地 HTTP/WebSocket 服务。
- `src/Ui/UiRequest.py`
  - 浏览器入口路由。
  - 对 `/<address>` 先返回 wrapper，再由 wrapper 加载 `/media/<address>/...`。
  - 缺失文件时调用 `site.needFile()` 触发下载。

### 发现与扩散

- `src/Site/SiteAnnouncer.py`
  - tracker announce 与 PEX 逻辑。
- `src/Peer/Peer.py`
  - 面向单个 peer 的请求封装。

## 为“能连 Python 节点并打开旧站点”必须保留的能力

### 必须实现

1. `v2` 握手兼容。
2. msgpack 消息兼容，`use_bin_type=true`。
3. 基本命令：
   - `ping`
   - `getFile`
   - `streamFile` 或兼容分块读取
   - `listModified`（第二阶段可接入）
4. 站点目录布局兼容：
   - `data/<site-address>/content.json`
   - 同站点静态文件层级
5. `content.json` 基本校验：
   - `address`
   - `inner_path`
   - `modified`
   - 文件路径合法性
   - 文件 `sha512`
6. 本地 HTTP 服务：
   - `/<address>`
   - `/<address>/<path>`
   - 缺文件时自动拉取并落盘

### 第一阶段明确不做

1. Tor / onion。
2. Python 版插件系统。
3. 完整的 ZeroFrame / WebSocket API。
4. 站点签名与发布。
5. tracker 全协议兼容。
6. SQLite 内容数据库缓存重建。

## 推荐的 Go 重写顺序

### 阶段 1：最小互通内核

目标：连上 Python ZeroNet 节点，下载并本地打开静态站点。

模块：

- `internal/config`
  - 运行参数、目录、bootstrap peers。
- `internal/zeronet/protocol`
  - msgpack 消息编解码。
  - ZeroNet 握手消息结构。
- `internal/zeronet/conn`
  - TCP client。
  - 握手、请求响应、多次 `getFile` 分块下载。
- `internal/zeronet/site`
  - 本地站点目录。
  - `content.json` 缓存。
  - 文件下载与 sha512 校验。
- `internal/httpui`
  - 极简 HTTP 文件服务。
  - 缺文件时触发下载。

验收标准：

1. `go run ./cmd/go-zeronet --peer 127.0.0.1:15441` 能启动本地 UI 服务。
2. 首次访问 `http://127.0.0.1:43110/<address>` 时，程序能按需下载根 `content.json` 和站点静态文件。
3. 浏览器能打开至少简单静态站点页面。

### 阶段 2：站点更新与多 peer

目标：不依赖单个 bootstrap peer。

模块：

- peer 池
- `pex`
- `listModified`
- content include 的懒加载
- 更完整的 `content.json` 规则处理

### 阶段 3：tracker 接入

目标：不依赖 Python 节点也能发现 peers。

模块：

- `udp://` tracker announce
- `http://.../announce`
- `zero://` tracker 协议

### 阶段 4：ZeroFrame 兼容层

目标：支持更多老站点前端功能。

模块：

- wrapper 页面
- `/Websocket`
- 最小 ZeroFrame API
- 用户身份/权限基础兼容

### 阶段 5：发布与签名

目标：Go 版可独立维护站点。

模块：

- Bitcoin message sign/verify 完整兼容
- `siteSign`
- `sitePublish`
- 更新广播

## 当前 AI 实施策略

本次先完成“阶段 1”的工程骨架和第一版实现，优先让后续 AI 能在已有结构上继续迭代，而不是重新建模代码组织。
