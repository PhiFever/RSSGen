---
name: rss-image-archiving
description: RSSGen 生成 feed 时把可信 CDN 图片同步上传到 Immich 专用相册，并通过 Miniflux 内置媒体代理提供访问
type: project
status: ready-for-agent
date: 2026-08-30
revised: 2026-10-23
---

# RSS 图片持久归档（Immich 后端）

## Problem Statement

RSSGen 当前生成 RSS/Atom XML，由 Miniflux 轮询并保存条目。文章正文虽然进入了 Miniflux，其中的图片仍然引用知乎、爱发电等站点的外部 CDN。只要源图片过期、被防盗链拦截或 CDN 地址失效，已经保存的历史文章也会失去图片。

Miniflux 内置 media proxy 只是在读者打开文章时由 Miniflux 服务端即时转发源图，不落盘，源 CDN 失效后同样取不到图。

部署主机上已经运行 Immich（独立 Compose 项目 `immich`，网络 `immich_default`，无公网域名）。Miniflux 有公网域名，RSSGen 与 Immich 均只在内网可达。用户希望复用 Immich 作为图片的持久存储，而不是在 RSSGen 内再实现一套媒体存储与文件服务。

## Solution

RSSGen 保持现有产品边界：route 抓取上游内容，pipeline 生成并缓存 RSS/Atom，Miniflux 继续轮询 RSSGen。pipeline 新增一个可选的媒体归档步骤：在 `feed.Generate` 之前扫描每个条目正文中的 `<img src>`，对命中可信 CDN 规则的 HTTPS 地址，**同步**完成「下载 → 上传到 Immich → 加入专用相册」，然后把 `src` 改写为该资源在 Immich 上的内网地址（携带相册分享链接 key）。

改写后的地址形如 `http://immich_server:2283/api/assets/{assetId}/original?key={shareKey}`，读者浏览器无法直接访问。Miniflux 在显示文章时用 media proxy 把这类 http 图片改写成自己公网域名下的签名代理地址，由 Miniflux 服务端经内网向 Immich 取图。Immich 是持久存储，Miniflux 只负责访问。

同步上传使 asset id 在 feed 发布前就已确定，因此不再需要原方案的 Redis/Asynq 任务队列、"先改写后下载"的 503 窗口、冷却重激活和 RSSGen 自有媒体 HTTP 服务。重启后的 URL 稳定性由确定性文件名保证：上传时文件名取源 URL 的哈希，归档前先按这个文件名在相册里查找，查到就直接复用已有 asset，不下载也不上传。没查到时（例如同一张图换了 URL）才上传，此时 Immich 按内容校验和去重，返回已有 asset 的 id。两者都不需要在 RSSGen 本地持久化 URL → asset 映射。

## User Stories

1. 作为 RSSGen 运维者，我希望新生成 feed 中引用的图片被持久保存到 Immich，使旧的 Miniflux 条目不再无限期依赖源 CDN。
2. 作为 RSSGen 运维者，我希望 RSSGen 继续正常提供 RSS 和 Atom，媒体归档不改变 RSSGen 的产品边界。
3. 作为 Miniflux 用户，我希望在公网通过 Miniflux 阅读时能看到归档图片，而 Immich 不需要暴露到公网。
4. 作为 RSSGen 运维者，我希望 feed 一旦发布，其中的图片地址就是最终地址，之后不会因下载完成而再变化。
5. 作为 RSSGen 运维者，我希望 Immich 不可用时保留旧的 feed 缓存，不让一次故障把一批条目以 CDN 原地址永久写进 Miniflux。
6. 作为 RSSGen 运维者，我希望单张源图失效（404、超限、类型不安全）只影响这一张图，该图保留原地址，feed 照常更新。
7. 作为 RSSGen 运维者，我希望 RSSGen 重启后同一张图得到同一个 Immich 地址，feed XML 不会因重启而变化。
8. 作为 RSSGen 运维者，我希望只有显式列入白名单的 CDN 才会被下载，使媒体下载器不能被当作任意 URL 下载器。
9. 作为 RSSGen 运维者，我希望通配 CDN 规则按 DNS label 边界匹配，既能覆盖不断变化的 CDN 子域，又不会误匹配仿冒域名。
10. 作为 RSSGen 运维者，我希望每一跳重定向都重新校验，使白名单主机不能把下载器重定向到别处。
11. 作为 RSSGen 运维者，我希望不安全格式和超大文件在上传前就被拒绝，使 Immich 相册保持有界、对浏览器安全。
12. 作为 RSSGen 运维者，我希望图片下载沿用发现它的 route 的代理和客户端指纹，但不携带 route 的 Cookie 和认证头。
13. 作为 Immich 用户，我希望 RSS 图片不出现在我的主时间线里。
14. 作为 RSSGen 运维者，我希望不配置媒体归档时行为与现在完全一致。
15. 作为 RSSGen 运维者，我希望配置不完整时启动失败，而不是静默改变 URL 策略。
16. 作为 RSSGen 运维者，我希望日志不泄露 API key、分享 key、源 URL 中的敏感 query 和代理凭证。
17. 作为 RSSGen 运维者，我希望在 `/status` 中看到图片归档的成功与失败计数，不必翻日志排查。
18. 作为 RSSGen 运维者，我希望 RSSGen 重启后不必把当前 feed 里的图片重新下载一遍。
19. 作为 backfill 使用者，我希望历史回填导入和重刷的文章也归档图片，并且能在不部署 Docker、只在宿主机上定期运行二进制的情况下使用。

## Implementation Decisions

### 处理位置与范围

- 作用于两条写入路径：feed 生成，以及配置中有 `media` 段时的 `backfill execute`、`backfill refresh`（见下文「backfill 接入」）。RSSGen 不会主动扫描或改写 Miniflux 中已有的条目，只有运维者对某个窗口显式执行 `backfill refresh` 时，才会用归档后的正文覆盖这些条目。
- 所有 route 共用同一个与 route 无关的媒体步骤。route 不需要自己处理图片。知乎（`zhihu.go` 中拼出的 `<img>`）和爱发电（`appendPics` 追加的 `<img>`，以及文章正文自带的 `<img>`）的图片最终都在 `FeedItem.Content` 里。
- pipeline 是编排边界，顺序为：`Route.Fetch` → 媒体步骤（改写 `FeedItem.Content`）→ `feed.Generate`（内含 bluemonday 消毒与序列化）→ 写 FeedCache。
  - 媒体步骤放在消毒之前，是因为当前消毒在 `generateAtom`/`generateRSS` 内部按条目执行。bluemonday 策略对 `img` 全局放行 `src`，所以消毒前后能找到的 `<img src>` 集合是一样的。改写后的 `http://` 地址在 `AllowURLSchemes("http", "https", "mailto")` 下能保留下来，消毒仍是最后一道防线。
- 只处理 `<img>` 的 `src`。CSS URL、`srcset`、enclosure、视频、音频、data URI 都不处理。
- HTTP 缓存未命中路径（`main.go` 的 `makeFeedHandler`）和后台 refresher 都经过 `pipeline.Refresh`，媒体行为一致，不另设媒体扫描调度。

### 白名单与下载

- 源 URL 必须是 HTTPS 且使用默认端口。主机匹配不区分大小写，按 DNS label 边界匹配：`*.zhimg.com` 匹配任意深度的子域，但不匹配顶级的 `zhimg.com`，也不匹配 `evilzhimg.com`；顶级域名需要时单独列出。不在白名单内的 URL 原样保留。
- 最多跟随 3 次重定向，每一跳都独立校验协议、端口和白名单。
- 不做 DNS 解析结果的地址类别校验（原方案有）：route 配了代理时由代理解析，本地拿不到实际连接地址，这项校验做不到位。信任边界是运维者显式列出的公共 CDN 白名单。
- 下载使用媒体专用的 `tls-client` 会话，沿用该 route 解析后的代理和指纹 profile（`config.ResolveRoute`），但不共享 scraper 的 Cookie Jar，不复制 Cookie、Authorization 或 route 的额外请求头。请求头固定：媒体 User-Agent、`Referer` 设为条目链接、`Accept-Encoding: identity`。
- 单张图片总超时 30 秒。单次 refresh 内并发下载上限为 2。媒体下载不占用文章抓取的限速器。
- 响应体经过 `max_size + 1` 的限流读取，`max_size` 默认 20 MiB，可配置。
- 只接受 JPEG、PNG、GIF、WebP、AVIF，以魔数检测为准，拒绝 SVG 及其他类型。`Content-Type` 与魔数冲突时记一条脱敏 warning，按魔数结果处理。

### 模块边界

- `internal/media` 分两层。
  - 通用层：白名单、下载、魔数识别、HTML 改写、内存表、并发编排。它不知道 Immich 的存在，只通过存储的四个操作工作：`find`（按文件名主名查找已提交的资源）、`put`（存入，拒收文件时返回 `errRejected`）、`commit`（提交，使资源可以被访问）、`url`（取发布地址）。
  - Immich 适配：全部 Immich 细节（接口路径、状态码含义、相册、分享 key、地址格式）都在 `immich.go` 的 `immichStore` 里。
- 现在只有一个后端，`immichStore` 是具体类型，不是接口。将来加第二种后端时，配置里与 `media.immich` 并列增加一段，再从实际的两种实现中抽出接口。通用配置项（`allowed_hosts`、`max_size_mb`）留在 `media` 下，不需要迁移。

### Immich 交互

- RSSGen 使用 Immich 专用用户的 API key（请求头 `x-api-key`）。该 key 只授予 `asset.upload` 和 `albumAsset.create` 权限。RSS 图片放在这个专用用户名下，不进入主用户的图库。
- 存在性检验用相册分享 key 鉴权（请求头 `x-immich-share-key`），不需要给 API key 增加 `asset.read`。每个请求只带一种鉴权头。分享 key 不放进 URL：Go 的请求错误会带上完整 URL，而错误文本会出现在日志和 `/status` 里。
- 相册和相册分享链接由运维者在 Immich 中手工创建，分享链接必须开启「允许下载」：`/api/assets/{id}/original` 走分享链接鉴权时需要 `asset.download` 权限。RSSGen 不自动创建相册或分享链接。实测（v3.3.4）：创建分享链接时 `showMetadata=false` 会让 Immich 把 `allowDownload` 强制设成 false，因此两项都要设为 true。
- 上传调用 `POST /api/assets`，multipart 字段为 `assetData`、`fileCreatedAt`、`fileModifiedAt`（两个时间都取条目发布时间，缺失时取当前时间）、`filename`、`visibility=archive`。设为 archive 是为了不进专用用户的时间线。
  - Immich 按 `filename` 的扩展名判断文件类型（实测：扩展名不支持时返回 400 `Unsupported file type`），而知乎、爱发电的图片 URL 不一定带正确扩展名。`filename` 必须用魔数检测出的类型拼扩展名（`.jpg`/`.png`/`.gif`/`.webp`/`.avif`）。主名取规范化源 URL 的 sha256 前 8 字节（16 位 hex），存在性检验依赖这个主名可以在下载前算出来。
  - 响应 201 表示新建，200 表示 Immich 按校验和判定为重复。两种情况都取响应中的 asset id。
- 每次 refresh 结束前，把本次新得到的 asset id 一次性 `PUT /api/albums/{albumId}/assets` 加入相册。该接口对逐个 id 返回结果，HTTP 状态码是 200：`success=true` 或 `error="duplicate"`（已在相册中）都视为成功；其他错误（如 `no_permission`、`not_found`）属于 Immich 侧失败。相册不存在时整体返回 400 `Album not found`。资源加入相册之前，用分享 key 访问 `/original` 返回 404，所以必须在加入相册成功之后才能发布改写后的地址。
- 存在性检验：内存表未命中的图片，下载前先调用 `POST /api/search/metadata`，请求体为 `{"albumIds":["<album_id>"],"originalFileName":"<主名>"}`。
  - 用分享 key 鉴权时必须带 `albumIds`，否则 Immich 返回 400。结果限定在这个相册内，能查到就说明资源已经入册，不再调用入册接口。
  - `originalFileName` 是包含匹配（实测传中间片段也能命中），所以客户端要比对：结果文件名去掉扩展名后必须与主名完全相等。下载前还不知道扩展名，只能按主名查。
  - 不传 `visibility`，也能查到 `visibility=archive` 的资源。
  - 命中就直接使用该 id，跳过下载、上传和入册。未命中才走下载 → 上传 → 入册。查询失败（连接失败、401 分享 key 错误等）属于 Immich 侧失败。
  - 代价是每张新图多一次内网查询。换来的是：重启后的首次 refresh、以及反复执行的 backfill 不再重新下载已归档的图片。
- 改写后的地址是 `{immich.url}/api/assets/{assetId}/original?key={shareKey}`。用原图而不是 `thumbnail?size=preview`，是为了保留 GIF 动画和原始格式。
- 进程内维护「规范化源 URL → asset id」的内存表，只在资源确认入册后写入（存在性检验命中，或加入相册成功），同一进程内同一张图不再重复查询。这张表不持久化，重启后由存在性检验重建。表不设上限，按每天新增图片数估算，一年也只有几 MB。
- URL 规范化：协议和主机转小写，去掉 fragment 和默认的 `:443`，路径和 query 原样保留。
- refresher 与 HTTP 缓存未命中路径可能同时上传同一张图，不加锁，由 Immich 去重收敛到同一个 id。
- Immich 客户端用标准库 `net/http` 直连，显式设置 `Proxy: nil`。RSSGen 容器从 Docker 继承了 `HTTP_PROXY=http://host.docker.internal:10801`，而 `NO_PROXY` 里没有 `immich_server`，用默认 Transport 会把内网请求发给宿主机代理。

### 失败处理

- **源侧失败只影响单张图**：下载网络错误或超时、CDN 返回非 2xx、重定向不合规、超限、类型不安全，以及上传接口 `POST /api/assets` 对该文件返回 400（文件本身被拒）时，这张图保留原地址，refresh 继续，记一条脱敏 warning 并计入 `/status`。下次 feed 重新生成时自然会再试一次，不另设重试机制。
- **Immich 侧失败让整次 refresh 失败**：连接失败、超时、5xx、401/403、相册接口的 400（相册不存在）、加入相册返回除 `duplicate` 以外的逐项错误、响应无法解析时，`pipeline.Refresh` 返回错误，不写 FeedCache，旧缓存保留，由 refresher 已有的重试和退避接手。目的是避免 Immich 故障期间把一批条目以 CDN 原地址写进 Miniflux。
- Immich 侧错误必须是普通 error，**不能**是 `*route.HTTPError`。否则 `health.RecordFailure` 会把 Immich 的 403 等状态码当成上游业务错误，禁用 feed 并发送告警。
- 不新增通知类型。Immich 故障通过 refresh 错误统计和日志体现；单图失败只记日志并计数。

### 配置

```yaml
media:
  immich:
    url: "http://immich_server:2283"   # RSSGen 调用 Immich API 的地址，也是改写进正文的地址，必须对 Miniflux 可达
    api_key: "..."                     # Immich 专用用户的 API key
    album_id: "..."                    # 专用相册 UUID
    share_key: "..."                   # 该相册分享链接的 key（需允许下载）
  allowed_hosts:
    - "*.zhimg.com"
    - "pic1.afdiancdn.com"
  max_size_mb: 20
```

- 没有 `media` 段时，媒体归档关闭，行为与现在完全一致。
- 有 `media` 段时，`immich.url`（绝对 URL）、`api_key`、`album_id`、`share_key` 和至少一条 `allowed_hosts` 缺一不可，校验失败则启动失败。通配规则只允许 `*.` 前缀形式。
- Docker 部署中 RSSGen 和 Miniflux 用同一个容器名访问 Immich，API 地址和正文地址相同。在宿主机上运行 backfill 时，RSSGen 只能通过 `http://localhost:2283` 访问 Immich，但写进正文的地址必须是 Miniflux 容器能访问的 `http://immich_server:2283`；这时在 `backfill.immich_url` 中写本机地址，只覆盖 backfill 调用 API 的地址，见「backfill 接入」。

### backfill 接入

- `backfill execute` 和 `backfill refresh` 读取的配置文件（`--config`，默认 `config.yml`，与 server 同一格式）中有 `media` 段时归档，没有则不归档。`backfill.immich_url` 非空时，backfill 调用 Immich API 用它，正文地址仍是 `media.immich.url`；写了 `immich_url` 却没有 `media` 段时报错。详见 `2026-10-23-backfill-config-file-design.md`。
  - 目的是支持不部署 Docker、只在宿主机上定期运行二进制的使用方式。配置复用 server 的格式，不另设一套环境变量。
  - 图片下载的指纹和代理取该配置中与 `--source` 同名 route 的 `ResolveRoute` 结果，与 server 下载同一个 route 的图片时一致。
- 归档发生在每篇文章拼接好正文和评论之后、写入 Miniflux 之前，Referer 取文章 URL，上传时间取文章发布时间。
- Immich 侧失败时，backfill 立即中止：`execute` 不导入当前及后续条目；`refresh` 记录 operation 为 `media` 的失败。backfill 本身可以安全重跑，这里不另设重试。源侧失败与 feed 路径相同，只影响单张图。
- dry-run 不归档。backfill 结束时在日志中输出一次归档计数。
- 密钥写在 `config.yml` 中，与现有的 Cookie 和 shoutrrr URL 同等对待（文件已被 gitignore），不另引入环境变量。
- 启动时**不**检查 Immich 是否可达。RSSGen 与 Immich 属于不同 Compose 项目，主机重启时启动顺序不确定；Immich 未就绪的影响由上面「Immich 侧失败」的规则承担。

### 日志与状态

- 日志中的源 URL 去掉 query 值，Immich 地址不输出 `key` 参数，不输出 API key 和代理凭证。
- `/status` 增加 `media` 段：`stored`（新存入数）、`deduplicated`（存储去重命中数）、`found`（存在性检验命中数）、`source_failures`（单图失败数，按原因分类）、`last_store_error` / `last_store_error_at`（最近一次存储侧错误）。字段名与后端无关。

### 部署

- 实际运行的部署文件是 `/opt/stacks/miniflux/compose.yaml`（仓库里的 `docker-compose.yml` 只是模板），两处需要同步修改：声明外部网络 `immich_default`（`external: true`），`rssgen` 和 `miniflux` 两个服务同时接入默认网络和该外部网络。
- Miniflux 的 `NO_PROXY` 增加 `immich_server`（线上已于 2026-10-23 加上）。
- Miniflux 增加稳定的 `MEDIA_PROXY_PRIVATE_KEY`（默认每次启动随机生成，重启后旧代理链接会失效），并显式写出 `MEDIA_PROXY_MODE=http-only` 和 `MEDIA_PROXY_RESOURCE_TYPES=image`（都是默认值，写出来是为了表明部署依赖它们）。保留 `FETCHER_ALLOW_PRIVATE_NETWORKS=1`。
- 必须把 Miniflux 的 `BASE_URL` 设为公网地址（线上已于 2026-10-23 设置；默认值是 `http://localhost`）。读者通过 Fever API 接第三方 app，Fever 接口返回的正文用 `mediaproxy.RewriteDocumentWithAbsoluteProxyURL` 改写，代理地址以 `BASE_URL` 为前缀（见 miniflux/v2 `internal/fever/handler.go`、`internal/mediaproxy/url.go`）。不设置的话，app 拿到的图片地址是 `http://localhost/proxy/...`，打不开。Web 界面使用相对代理地址，不受影响。
- Immich 侧准备：创建专用用户 → 用该用户创建 API key（只授予 `asset.upload`、`albumAsset.create`）→ 创建相册 → 为相册创建允许下载的分享链接。
  - 2026-10-23 已完成：专用用户 `rssgen`（非管理员），相册「RSSGen 图床」（`album_id` 为 `45630f4a-1459-4dd5-afdd-2a20a10df8ee`），以及该相册允许下载的分享链接。相册里有 3 张测试图。
  - 测试用的是全权限 key（放在仓库根目录 `.env`，已被 gitignore）。上线前要另建一个只有两项权限的 key 写进 `config.yml`。

## Testing Decisions

- 主要的测试接缝是 `media` 包对一次 feed 结果做归档的导出入口，断言外部可观察的结果：改写后的 `Content`、假 Immich 收到的请求、返回的错误。不断言内部函数调用。
  - 源站：白名单规则要求 HTTPS 和默认端口，httptest 起不了这样的服务器，所以源站请求通过包内一个不导出的源客户端字段替换成假实现（按 URL 返回状态码、响应头和响应体）。这是本功能唯一的测试替身。
  - Immich：用 `httptest` 起真实的 HTTP 服务器。
- pipeline 只多了一处接线，沿用现有的 pipeline 测试：未配置媒体归档时行为不变。归档出错时 `Refresh` 不写缓存，由 `Refresh` 的错误分支保证，与其他步骤出错时走的是同一个分支。
- 主路径：白名单内的图片被改写为 `{immich.url}/api/assets/{id}/original?key=...`，假 Immich 收到了上传请求（`filename` 扩展名与魔数类型一致）和加入相册请求；白名单外的图片和其余 HTML 原样保留。另有一条 feed 级测试，确认改写后的 `http://` 地址经过 `feed.Generate` 的消毒后在 Atom 和 RSS 中都还在。
- 去重：假 Immich 对同一内容返回 200 和已有 id 时，生成的地址与首次相同（模拟换了 URL 的同一张图）。
- 存在性检验：
  - 命中时，不请求源站、上传和入册接口，只用分享 key 鉴权；
  - 文件名包含主名但不完全相等时，按未命中处理；
  - 查询返回 401 时，归档返回错误，正文保持不变。
- API 地址覆盖：设置了 `APIURL` 时，API 请求发往 `APIURL`，改写进正文的仍是 `url`。
- backfill：`Archive` 依赖不为 nil 时，导入和重刷写入的都是归档后的正文；归档出错时中止。`backfill.immich_url` 非空但没有 `media` 段时报错。
- 源侧失败：CDN 返回 404 时该图保留原地址，归档不返回错误。
- Immich 侧失败：假 Immich 返回 503 时归档返回错误，错误不是 `*route.HTTPError`；加入相册返回非 `duplicate` 的逐项错误时同样返回错误；加入相册失败的图不写进内存表。
- 安全规则（需求明确要求）：重定向到白名单外主机被拒；超过 `max_size` 被拒；SVG 被拒；三种情况都保留原地址。
- Immich 客户端设置 `HTTP_PROXY` 指向一个不可达地址时，仍能直连假 Immich。
- 配置：`media` 段缺字段时加载报错。沿用现有 YAML 加载测试。
- 常规测试不访问真实 CDN、Immich、Miniflux 和代理。

### 部署冒烟（手工，启用前完成）

1. 用专用用户上传一批（几十张）RSS 图片，观察树莓派上 Immich 生成缩略图的 CPU 负载，确认可以接受。线上 Immich 用的是远程机器学习服务，智能搜索、人脸识别的计算不在树莓派上，但每张新图仍会产生一次远程推理请求。
2. ~~确认 `visibility=archive` 的资源能通过分享链接 `key` 访问 `/original`。~~ 2026-10-23 已验证，见 Further Notes。
3. 确认 GIF 经 Miniflux 代理后仍是动图。（`/original` 返回的字节与上传文件一致，已验证；还差 Miniflux 代理这一段。）
4. 完整链路：Miniflux 轮询 feed，Web 界面和通过 Fever API 接入的 app 都能经公网 Miniflux 看到图片；重启 RSSGen 后同一 feed 的 XML 不变。
5. 观察 RSSGen 重启后首次 HTTP 缓存未命中时的耗时。如果超过 Miniflux 的抓取超时（线上 `HTTP_CLIENT_TIMEOUT=120` 秒），确认 refresh 在后台完成并写入缓存，下次轮询能命中缓存。

## Out of Scope

- 用 Miniflux-only collector 或守护进程取代 RSS/Atom 轮询；把新条目直接导入 Miniflux。
- 主动扫描或改写 Miniflux 中已有的条目。只有运维者显式执行 `backfill refresh` 时才会覆盖已有条目，见「backfill 接入」。
- 归档白名单 CDN 以外的任意 URL。
- 归档 CSS 背景、`srcset`、RSS/Atom enclosure、视频、音频、SVG 或 data URI。
- 由 RSSGen 自动创建或维护 Immich 用户、相册、分享链接。
- 删除、过期或回收 Immich 中的资源。运维者不应删除该相册中的资源：删除后历史文章的图片会永久失效，而重新上传得到的是新 id。
- 持久化 URL → asset 映射，以及异步任务队列、手动重试 CLI、失败任务数据库。
- 把 Immich 或 RSSGen 暴露到公网。
- 保证原始 feed URL 在本 Miniflux 部署之外的阅读器中也能显示归档图片。
- 依赖 Miniflux 在后续轮询中更新已存条目的正文。单图失败后的重新尝试只对 Miniflux 尚未保存的条目确定有效；Miniflux 是否会用新正文覆盖已存条目，未作为本设计的前提。

## Further Notes

- 核心不变量：**RSSGen 发布的 feed 中，每个被改写的图片地址都指向一个已上传、并已加入分享相册的 Immich 资源。** 同步上传让这一点在发布前就能确认。
- 第二个不变量：**同一张图片在重启前后得到同一个地址**。这由确定性文件名加存在性检验保证，Immich 按内容去重兜底，不依赖 RSSGen 的本地状态。
- 已知代价：
  - 每次重启后，当前 feed 中的每张图要做一次内网存在性查询，不再重新下载。
  - refresh 耗时增加，增加的部分是图片下载和上传的时间。
  - 每张新图都会触发 Immich 生成缩略图（在树莓派上）和机器学习任务（在远程机器学习服务上），见冒烟第 1 项。
  - 与 Immich API 耦合：Immich 的 API 历史上有过破坏性变更，当前运行版本为 v3.3.4，升级 Immich 后应重跑冒烟。
- 分享 key 会出现在 feed XML 中，也会以 base64 形式出现在 Miniflux 代理地址里。它只能读取这一个相册，而且 Immich 不在公网，外部拿到 key 也无法直接使用；能拿到代理地址的人本来就能看到这张图。
- Immich API 的行为（上传字段、200/201 的含义、`x-api-key` 与 `key` 鉴权、`asset.download` 需要分享链接允许下载、`albumAsset.create` 权限、visibility 取值）和 Miniflux media proxy 的配置项，于 2026-10-23 依据官方文档核对，并在线上 Immich v3.3.4 上实测：
  - 新上传返回 201 `{"id","status":"created"}`；内容相同的再次上传返回 200 `{"status":"duplicate","id":<已有 id>}`。
  - 只有 `asset.upload` 和 `albumAsset.create` 两项权限的 key，上传、重复上传、加入相册都能成功；用它读 asset 详情返回 403。
  - `visibility=archive` 的资源不出现在时间线里，加入相册后可以只凭分享 key 访问 `/original`。返回的字节与上传的文件一致，PNG 和 GIF 都验证过。响应头为 `Content-Disposition: inline`、`Cache-Control: private, max-age=86400`。
  - 分享 key 错误时返回 401，资源未加入相册时返回 404。

### 修订记录

- 2026-08-30：初版。RSSGen 本地文件存储 + Redis/Asynq 异步下载 + RSSGen `/media/v1` 文件服务，"先确保任务落盘，再改写 URL"。
- 2026-10-23：存储后端改为 Immich 专用相册，下载改为在 pipeline 内同步完成。
  - 删除：Redis/Asynq、持久化任务、503 未就绪窗口、24 小时冷却重激活、媒体文件卷与分片目录、`/media/v1` handler、三种配置模式、`REDIS_PASSWORD`、媒体失败通知、DNS 地址类别校验、按主机的媒体限速。
  - 原因：Immich 的 asset id 要上传后才能拿到，套不进原方案"先改写后下载"的模型；改为同步后，持久化任务和就绪窗口都不再需要。Immich 本身负责持久存储和文件服务，并通过内容去重保证 URL 跨重启稳定。
- 2026-10-23（二）：增加存在性检验、`public_url` 和 backfill 接入。
  - 存在性检验：原以为按文件名查询要给 API key 增加 `asset.read` 权限，所以当初只靠 Immich 内容去重，代价是重启后要把图片全部重新下载。实测发现分享 key 加相册过滤就能查询，不需要扩大 API key 的权限，于是改为先查。
  - backfill：历史导入和重刷原本不经过归档；按需求改为可选接入，并支持宿主机运行。
- 2026-10-23（三）：删除 `public_url`，改由 `backfill.immich_url` 覆盖 backfill 调用 API 的地址；backfill 改为配置中有 `media` 段就归档。
  - 原因：`public_url` 唯一的用途是宿主机上的 backfill，却让 server 与 backfill 无法共用同一份 `media` 段。backfill 改为只读配置文件后（见 `2026-10-23-backfill-config-file-design.md`），把宿主机地址放进 `backfill` 段，`media.immich.url` 就恢复为单一含义。
