# 爱发电路由冷启动延时优化设计

## 问题背景

当前爱发电路由在缓存为空时（冷启动），`fetch()` 方法需要串行执行大量 API 调用：

1. 1 次调用获取 `author_id`
2. 2+ 次翻页调用获取文章列表（默认 20 篇，per_page=10）
3. 20 次调用逐篇获取文章详情

总计约 23 次 API 调用，加上每次 0.5 秒的 rate_limit sleep，冷启动耗时至少 12 秒，很容易超过 Miniflux 默认的 30 秒超时。

## 设计约束

- **完整正文**：RSS 中必须包含每篇文章的完整正文，不能用列表 API 的截断摘要替代
- **数据时效性**：可接受最多 12 小时的延迟
- **保守访问**：串行请求爱发电 API，不做并发，避免触发风控
- **文章可变**：作者可能编辑已发布的文章，正文缓存需要有 TTL

## 方案选择

评估了三种方案：

| 方案 | 思路 | 优点 | 缺点 |
|------|------|------|------|
| A. 后台定时刷新 | 预配置 feed 列表，启动预热 + 定时刷新 | 简单，请求零延迟 | 无法动态发现新订阅 |
| B. 请求触发 + Stale-While-Revalidate | 首次请求触发后台抓取，返回空 feed | 按需发现，无需预配置 | 首次请求无数据 |
| **C. 混合方案（采用）** | 已知 feed 预热 + 定时刷新；未知 feed 按需触发 + 文章级缓存 | 兼顾零冷启动和灵活性 | 实现较复杂 |

**选择方案 C**，因为：
1. 使用场景是通过 Miniflux 订阅固定创作者，这些 slug 可预配置，启动即预热
2. 文章级缓存（12h TTL）使每次刷新只需获取少量新/变更文章的详情
3. 保留动态访问能力

## 架构设计

### 整体架构：读写分离

将数据抓取从 HTTP 请求中完全解耦：

```
写入路径（后台）：
  启动预热 / 定时调度器
    → BackgroundRefresher 串行调用 Route.fetch()
    → 文章正文写入 ArticleCache（按 post_id，TTL 12h）
    → 生成的 feed XML 写入 FeedCache（按 cache_key，TTL 6h）

读取路径（请求）：
  Miniflux GET /feed/afdian/{slug}
    → FeedCache 命中 → 直接返回 XML
    → FeedCache 未命中 → 触发后台抓取任务，返回空 feed（HTTP 200 + 空 Atom）
```

### 双层缓存

继续使用 `cachetools.TTLCache`（内存缓存），不引入 Redis。`Cache` 类接口不变，新增独立的 ArticleCache 实例。

**FeedCache（Feed 级缓存）：**
- Key：`feed:{route_name}/{path}?{query}`
- Value：完整的 Atom/RSS XML 字符串
- TTL：可配置，默认 6 小时
- 作用：HTTP 请求直接读取，零延迟返回

**ArticleCache（文章级缓存）：**
- Key：`article:{route_name}:{post_id}`
- Value：文章正文 HTML 字符串
- TTL：可配置，默认 12 小时
- 作用：后台刷新时，只对缓存过期/不存在的文章调用 `get-detail` API

**刷新时的调用流程：**
```
拉取文章列表（1-2 次 API 调用）
  → 遍历每篇文章
    → ArticleCache 命中且未过期 → 跳过，使用缓存正文
    → ArticleCache 未命中或已过期 → 调用 get-detail → 写入 ArticleCache
  → 用所有文章正文生成 XML → 写入 FeedCache
```

### 后台调度器

新增 `RSSGen/core/refresher.py`，`BackgroundRefresher` 类：

```python
class BackgroundRefresher:
    def __init__(self, feed_cache, article_cache, config):
        self._task: asyncio.Task | None = None       # 主循环任务
        self._pending: set[str] = set()               # 正在刷新的 cache_key，防止重复触发
        self._error_status: dict[str, dict] = {}      # 每个 feed 的刷新状态

    async def start(self):
        """启动时调用，开始预热 + 定时循环"""
        self._task = asyncio.create_task(self._run_loop())

    async def stop(self):
        """关闭时调用，取消任务"""
        if self._task:
            self._task.cancel()

    async def trigger(self, route_name, path_params, query_params):
        """动态触发：未知 feed 首次访问时调用，非阻塞"""
        cache_key = f"{route_name}/{'/'.join(path_params)}"
        if cache_key not in self._pending:
            asyncio.create_task(self._refresh_one(route_name, path_params, query_params))

    def get_status(self) -> dict:
        """返回所有 feed 的刷新状态"""
        return self._error_status
```

**调度策略：**
- 启动时立即对所有已配置的 feed 执行一轮预热（串行）
- 预热完成后进入定时循环，每隔 `refresh_interval`（默认 4 小时）全量刷新
- 使用 `asyncio.create_task` + `asyncio.sleep` 实现，不引入额外调度库

### 请求处理层改造

`app.py` 中 `/feed/{route_name}/{path}` 端点改为只读缓存：

- FeedCache 命中 → 直接返回 XML
- FeedCache 未命中 → 调用 `refresher.trigger()` 触发后台抓取（非阻塞），生成空 Atom feed 返回
- 返回 HTTP 200（非 502/503），避免 Miniflux 将订阅标记为错误
- 空 feed 是合法 Atom XML，Miniflux 正常解析，只是没有条目
- 请求路径不再直接调用 `route.fetch()`

### 爱发电路由改造

`afdian.py` 的 `fetch()` 方法新增可选的 `article_cache` 参数：

- 遍历文章时先查 ArticleCache，命中则跳过 API 调用和 rate_limit sleep
- 未命中才调用 `get-detail` 并写入缓存
- `article_cache` 为可选参数，保持 `Route` 基类接口不变

**典型场景下的效果：**

| 场景 | API 调用次数 | 预估耗时 |
|------|-------------|---------|
| 冷启动（全部 miss） | 1 + 2 + 20 = 23 | ~12s（后台执行） |
| 定时刷新（无新文章） | 1 + 2 + 0 = 3 | ~1.5s |
| 定时刷新（2 篇新文章） | 1 + 2 + 2 = 5 | ~2.5s |
| HTTP 请求（缓存命中） | 0 | <1ms |

### 错误处理与容错

**后台刷新失败时的行为：**
- **单篇文章详情获取失败**：记录日志，跳过该文章，继续处理剩余文章
- **文章列表 API 失败**：记录日志，本轮刷新终止，FeedCache 中的旧 XML 保留不动
- **Cookie 失效 / 403**：记录 warning 级别日志，刷新终止，保留旧缓存

**容错策略：有限容忍，不做无限兜底**
- FeedCache TTL（6 小时）本身是容忍窗口，一两轮刷新失败时旧缓存仍在
- FeedCache 自然过期后如果仍未成功刷新，返回空 feed
- 不引入额外的 stale cache 机制

**错误上报：**
- `BackgroundRefresher` 维护 `_error_status` 字典，记录每个 feed 最近一次刷新的状态
- 新增 `GET /status` 端点，展示所有 feed 的刷新状态（最后成功时间、错误信息）
- 用户或监控工具通过该端点发现问题

**日志级别：**
- 正常刷新：info，记录耗时和 API 调用次数
- 部分失败：warning，记录跳过的文章
- 全部失败：error，记录异常信息

## 配置变更

`config.yml` 中 `routes.afdian` 新增配置项：

```yaml
routes:
  afdian:
    enabled: true
    cookie: "..."
    rate_limit: 0.5             # 已有：请求间隔（秒）

    # 新增配置项
    refresh_interval: 14400     # 后台刷新间隔，默认 4 小时（秒）
    article_ttl: 43200          # 文章级缓存 TTL，默认 12 小时（秒）
    feed_ttl: 21600             # Feed 级缓存 TTL，默认 6 小时（秒）
    feeds:                      # 预热列表（可选，不配置则无预热）
      - slug: "author1"
        limit: 20
      - slug: "author2"
        limit: 10
```

## 需要变更的文件

| 文件 | 变更内容 |
|------|---------|
| `config.example.yml` | 新增 refresh_interval、article_ttl、feed_ttl、feeds 配置项及注释 |
| `RSSGen/core/cache.py` | 无改动（复用现有 Cache 类，不同 TTL 实例化） |
| `RSSGen/core/route.py` | `fetch()` 签名新增可选 `article_cache` 参数 |
| `RSSGen/core/refresher.py` | 新建，BackgroundRefresher 后台调度器 |
| `RSSGen/routes/afdian.py` | 适配文章级缓存逻辑 |
| `RSSGen/app.py` | 启动预热、注册调度器、改造请求处理、新增 /status 端点 |
