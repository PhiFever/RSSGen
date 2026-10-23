# RSSGen

自托管 RSS 源生成框架，将任意网站转为标准 RSS/Atom 订阅源，通过 Docker Compose 与 Miniflux 阅读器集成部署。

## 快速开始

```bash
# 1. 复制配置文件并填入凭证
cp config.yml.example config.yml

# 2. Docker 一键部署（含 Miniflux + PostgreSQL）
docker compose up -d

# 3. 访问 Miniflux
# http://localhost:8080 (默认账号 admin / admin123)
# RSSGen 仅在 Docker 内部网络中可用，不对外暴露端口
```

本地开发：

```bash
go run . server
```

直接运行 `go run .` 会显示可用子命令和帮助信息。

## 支持的路由

### 爱发电 (Afdian)

订阅爱发电创作者的动态更新。

**订阅地址：** `http://localhost:8000/feed/afdian/{作者url_slug}`

其中 `{作者url_slug}` 是作者主页 URL 中的标识，例如作者主页为 `https://afdian.com/a/Alice`，则 slug 为 `Alice`。

**配置（config.yml）：**

```yaml
routes:
  afdian:
    enabled: true
    cookie: "你的爱发电 Cookie"
```

**获取 Cookie：**

推荐使用 [Cookie Master](https://chromewebstore.google.com/detail/cookie-master) 浏览器扩展：

1. 浏览器登录 [afdian.com](https://afdian.com)
2. 点击 Cookie Master 图标 → **Flat Copy**
3. 将复制的内容直接粘贴到 `config.yml` 中：

```yaml
routes:
  afdian:
    cookie: "_ga=GA1.1.xxx; auth_token=xxx; ..."
```

也可以通过开发者工具（F12）→ 网络标签页 → 任意请求的 `Cookie` 请求头中复制，格式相同。

**在 Miniflux 中使用：**

在 Miniflux 添加订阅时，填入 `http://rssgen:8000/feed/afdian/{作者url_slug}`（Docker 网络内使用容器名 `rssgen`）。

**注意：** 如果宿主机配置了 HTTP 代理（`HTTP_PROXY`/`HTTPS_PROXY`），Docker 容器可能会继承代理设置，导致 Miniflux 无法通过 Docker 内部域名访问 RSSGen（返回 502）。`docker-compose.yml` 中已通过 `NO_PROXY` 环境变量排除内部服务，如有自定义服务名请一并添加。

### Afdian 历史回填

使用 `server` 子命令启动 RSS server，feed 默认返回最新 20 篇。付费后需要把完整历史补进已有 Miniflux feed 时，显式运行一次性前台命令。该命令默认不读取 `config.yml`（归档图片时用 `--config` 显式指定，见「回填时归档图片」），但会自动尝试加载当前工作目录下的 `.env`。

```bash
# 创建本地凭证文件（.env 已被 Git 忽略）
cp .env.example .env
# 然后编辑 .env，填写 MINIFLUX_URL、MINIFLUX_API_TOKEN 和 AFDIAN_COOKIE（知乎回填使用 ZHIHU_COOKIE）

# 1. 查询可回填的 Afdian feed ID
go run . backfill list --source afdian \
  --miniflux-url http://localhost:8080

# 2. 完整扫描并预览缺失数量，不请求正文、评论或写入 Miniflux
go run . backfill execute --source afdian \
  --miniflux-url http://localhost:8080 \
  --feed-id 42 \
  --dry-run

# 3. 按新到旧补齐全部缺失历史
go run . backfill execute --source afdian \
  --miniflux-url http://localhost:8080 \
  --feed-id 42
```

`.env` 是可选的；也可以继续直接设置进程环境变量，且进程环境变量优先于 `.env` 中的同名值。回填从目标 feed 的 `feed_url` 推导作者 slug，不需要重复传参。运行期间会持续输出阶段切换、Afdian 分页扫描累计数、对账结果和逐篇导入进度。Afdian 请求严格串行，默认最小间隔为 1 秒；可以用 `--request-interval 2s` 调慢，但不能低于 1 秒。任务可安全重跑，Miniflux 中已有条目会被跳过。目标 Miniflux 需要支持 Entry Import（Miniflux 2.2.16 或更高版本）。

### 知乎历史回填

知乎回填从用户动态列表向前翻页，默认最多扫描 1000 条动态（`--max-items`，`0` 表示不限制）。动态流不会自然收敛——实测单个账号翻过 7674 条仍未出现结束标记——所以这个上限是必要的刹车，触发时会在日志中明确告知"已达知乎历史扫描上限"，而不是静默截断。命令同样尝试读取 `.env`；从 `.env.example` 复制后，在 `.env` 中填写 `MINIFLUX_URL`、`MINIFLUX_API_TOKEN` 和 `ZHIHU_COOKIE`。执行与 dry-run 所用的 `ZHIHU_COOKIE` 必须含 `d_c0`，只列出 feed 时不需要 cookie。凭证仅通过环境变量或 `.env` 提供。

```bash
# 1. 查询可回填的知乎 feed ID
go run . backfill list --source zhihu \
  --miniflux-url http://localhost:8080

# 2. 扫描并预览缺失数量，不写入 Miniflux
go run . backfill execute --source zhihu \
  --miniflux-url http://localhost:8080 \
  --feed-id 42 \
  --dry-run

# 2b. 放开条数上限，翻到上游自然结束（大号可能是数千次请求）
go run . backfill execute --source zhihu \
  --miniflux-url http://localhost:8080 \
  --feed-id 42 \
  --max-items 0 \
  --dry-run

# 3. 按新到旧补齐缺失历史
go run . backfill execute --source zhihu \
  --miniflux-url http://localhost:8080 \
  --feed-id 42
```

默认按目标 feed URL 上的 `include` 过滤；`--include answer,article,pin` 可覆盖 URL 中的类别列表。合法取值为 `answer`、`article`、`pin`、`collected_answer`、`collected_article`、`collected_pin`、`voteup_answer`、`voteup_article`、`followed_question`、`followed_collection`、`voteup_pin`、`asked_question`，逗号分隔并自动去空白、去重和排序。两处均未指定类别时，知乎回落到作者本人创作的 `answer`、`article`、`pin`；要回填收藏、点赞、关注等类别必须用 `--include` 显式列出。该默认值与 `config.yml` 中 `default_include` 的取值相同但各自维护——命令不读取 server 配置中的路由设置，改了一处不会同步到另一处。非法类别会在历史扫描之前报错。未识别的动态类型其类别取上游的 `target.type`，不在默认类别内，因此默认不再回填；上游出现新类型时靠日志中的「未识别的知乎动态」告警发现，该告警在类别过滤之前触发。缺失数量按类别显示为 `missing_by_category`。

`--include-self-interaction` 默认关闭，默认跳过作者对自己内容的赞同和收藏；显式传入可保留这些互动。这两个选项、`--max-items`、`--start-before` 和 `--stop-after` 仅适用于知乎的 `execute` 与 `refresh`；`list` 不接受扫描参数。正文直接复用动态列表与 RSS 的渲染结果，空正文保持为空，不另行抓取正文或评论。请求串行且默认至少间隔 1 秒，可用 `--request-interval 2s` 调慢。建议先 dry-run 检查上游动态流实际保留的历史范围，再执行导入；已存在的 URL 或 GUID 会被跳过。

`--start-before` 指定从哪个游标向更早的动态扫描，接受两种形态：纯数字毫秒游标（至少 `1000000000000`，例如 `1781935272669`），或日期（`2026-06-01` 按 UTC 零点解释，也接受 `2026-06-01T00:00:00Z` 这样的 RFC3339 时间）。日期沿用既有解析：取整秒后乘以 1000 转为毫秒游标，不保留 RFC3339 小数秒。不传时从最新一页开始。

每页「知乎历史扫描进度」日志包含 `next_offset=`。复制其中的非空值传回 `--start-before`，即可继续扫描更早的动态；与 `--max-items` 组合即为按窗口分批扫描：

```bash
# 从指定日期向前扫描一个窗口并导入
go run . backfill execute --source zhihu \
  --miniflux-url http://localhost:8080 --feed-id 42 \
  --start-before 2026-07-01 --max-items 1000

# 从上一窗口日志复制 next_offset 的值，扫描下一个窗口
go run . backfill execute --source zhihu \
  --miniflux-url http://localhost:8080 --feed-id 42 \
  --start-before 1781935272669 --max-items 1000
```

扫描完成后才开始导入；中断扫描后直接使用最后的游标，会跳过本次已扫描但尚未导入的内容。要补齐这些内容，应重新执行该窗口原来的命令。`--max-items` 的上限落在一页中间时，该页剩余动态会被丢弃，此时应以「已达知乎历史扫描上限」那行的 `next_offset` 续扫——它取最后一条**被保留**的动态，不是整页末尾，因此被丢弃的条目会落进下一个窗口，不需要重叠补扫。系统不保存本地断点状态。

单页遇到网络错误或 HTTP 5xx 最多尝试 3 次，失败后依次等待 1 秒、2 秒；HTTP 4xx 和 JSON 解析错误直接返回。4xx 通常涉及签名失效或风控，反复请求可能加重风控。

### 知乎历史重刷

`backfill refresh` 用当前渲染器覆盖已有条目的标题与正文，修复旧版想法引用链接等历史渲染问题；缺失条目不会导入。当前仅知乎支持重刷，爱发电会明确报错拒绝。

重刷必须提供有效的 `--stop-after`，或显式指定正整数 `--max-items`。默认的 1000 条与显式 `--max-items 0` 都不算范围确认。`--stop-after` 也可用于 `execute`：扫描按动态游标由新到旧进行，保留等于下界的动态，遇到小于下界的动态就停止。上下界接受相同的毫秒游标或日期格式；上界不得早于下界，相等允许。仅给下界时仍保留默认 1000 条上限，确需扫描整个日期窗口可同时指定 `--max-items 0`。

```bash
# 先核对窗口内待更新的 Miniflux 条目 ID 与链接
go run . backfill refresh --source zhihu \
  --miniflux-url http://localhost:8080 --feed-id 59 \
  --start-before 2022-01-01 --stop-after 2020-01-01 --max-items 0 \
  --include pin --dry-run

# 确认清单后，在同一窗口实际重刷
go run . backfill refresh --source zhihu \
  --miniflux-url http://localhost:8080 --feed-id 59 \
  --start-before 2022-01-01 --stop-after 2020-01-01 --max-items 0 \
  --include pin
```

重刷输出已确认成功的更新数与失败条目详情；评论抓取最终失败的条目跳过，不用缺失评论的正文覆盖旧内容。到达下界表示指定窗口扫描完成，不提示继续扫描更早历史。`list`、`execute`、`refresh` 共用 `--source`、`--miniflux-url` 和 `--request-interval`，公共参数可放在子命令前后；`list` 只需要 Miniflux token，不要求数据源 Cookie。`--miniflux-url` 未传时回落到环境变量或 `.env` 中的 `MINIFLUX_URL`，两者都没有才报错；显式传参优先，便于临时指向另一个实例。

### 回填时归档图片

`execute` 和 `refresh` 传入 `--config <path>` 时，会读取该文件中的 `media` 段。每篇文章在写入 Miniflux 前，正文里白名单 CDN 的图片会先归档到 Immich 相册，规则与 server 生成 feed 时相同。指定的文件没有 `media` 段时，命令会直接报错。不传 `--config` 时行为不变。

在宿主机上运行时，Immich 要通过映射到宿主机的端口访问，但写进正文的地址必须是 Miniflux 容器能访问的地址。这时用 `public_url` 把两者分开：

```yaml
# backfill-media.yml（含 API key 与分享 key，注意文件权限）
media:
  immich:
    url: "http://localhost:2283"              # 本机调用 Immich API 的地址
    public_url: "http://immich_server:2283"   # 写进正文、由 Miniflux 访问的地址
    api_key: "..."
    album_id: "..."
    share_key: "..."
  allowed_hosts:
    - "*.zhimg.com"
    - "pic1.afdiancdn.com"
```

```bash
go run . backfill execute --source zhihu \
  --miniflux-url http://localhost:8080 --feed-id 42 \
  --config backfill-media.yml
```

- 图片下载使用该文件中与 `--source` 同名路由的指纹和代理设置。
- Immich 不可用时命令会立即中止；重跑是安全的，已导入的条目会被跳过。
- 单张源图下载失败时，这张图保留原地址，导入继续。
- 已经归档过的图片按文件名在相册里查到后会直接复用，所以对同一窗口反复执行 `refresh` 不会重新下载。
- dry-run 不归档。

## 后台刷新

RSSGen 会把本进程中真实访问过的 feed 动态加入后台刷新列表，避免只依赖 `config.yml` 手动维护预热列表。动态列表仅保存在内存中，重启后会重新学习；每个路由默认最多记录 200 个动态 feed，超过后按最久未访问淘汰。

```yaml
refresher:
  dynamic_feed_limit: 200 # 每个路由的动态 feed 上限；0 = 禁用动态学习
```

**查询参数：**

| 参数 | 说明 | 示例 |
|------|------|------|
| `format` | 输出格式，`atom`（默认）或 `rss` | `?format=rss` |
| `limit` | 返回条目数量，默认 20 | `?limit=10` |

## 故障通知

当后台刷新某个订阅源连续重试均失败、且为**业务错误**（默认 4xx：400/401/403/404/410/422/451）时，RSSGen 会：

1. 通过已配置的通知服务发送一条通知（支持飞书、ntfy 等，基于 shoutrrr URL）；
2. **禁用该订阅源**（feed 级，仅影响出错的那个 feed，同一路由下其他 feed 不受影响），后续 Miniflux 拉取该地址将返回 HTTP 502；
3. 重启 RSSGen 后自动恢复（禁用状态仅存于内存）。

临时错误（5xx、网络错误）只会重试，不触发通知或禁用。

**配置（config.yml）：**

```yaml
notifier:
  enabled: true            # 是否启用通知
  service_urls:   # shoutrrr 通知 URL，每行一个目标
    # 飞书机器人：webhook 地址 /open-apis/bot/v2/hook/ 后面那段 ID；secret 可选（签名校验密钥）
    - "lark://open.feishu.cn/你的webhook_id?secret=签名密钥"
    # ntfy：title/priority/tags 写在 URL 里；自建且只开 HTTP 时加 scheme=http
    - "ntfy://ntfy.example.com/RSSGen?token=tk_你的token&title=RSSGen%20订阅源获取失败&priority=4&tags=warning"
  # business_error_codes:  # 可选，自定义业务错误状态码（默认 [400, 401, 403, 404, 410, 422, 451]）
  #   - 403
```

旧的 `services`（`type` / `webhook_url` / `secret`）写法已移除，飞书配置需改写为 `lark` URL；启用通知时 URL 写错会导致启动失败，错误提示对应条目的序号（从 0 开始）。ntfy 的 topic 在首次发布时自动创建，无需预建。

`/status` 会返回后台刷新器是否启用，以及按路由和 feed 分组的最近刷新状态。

## 致谢

感谢 [cv-cat/ZhihuApis: 知乎算法逆向](https://github.com/cv-cat/ZhihuApis) 关于知乎路由的启发
