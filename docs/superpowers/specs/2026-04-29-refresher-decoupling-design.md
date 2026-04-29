---
name: refresher-decoupling
description: 后台调度器解耦重构 —— 从 afdian 专用改造为路由无关的通用调度器，并支持预热可关闭
type: project
---

# 后台调度器解耦与可配置化

## 背景与目标

当前 `BackgroundRefresher`（`RSSGen/core/refresher.py`）在多处硬编码 afdian：
- `_preinit_curl_cffi` 硬编码目标 URL `https://afdian.com/`
- `_run_loop` 硬编码读取 `config["routes"]["afdian"]["refresh_interval"]`
- `_refresh_feeds` 硬编码遍历 `config["routes"]["afdian"]["feeds"]`，硬编码 `feed_conf["slug"]` 作为 path_params
- `_refresh_one` 硬编码 `fetch_kwargs={"limit": ...}`

副作用：尽管 zhihu 路由已配置 `feeds:`，但 refresher 完全没有覆盖 zhihu —— 只有 afdian 走了预热和定时刷新。

此外，预热在每次进程启动时都会全量执行一遍，对已有 SQLite 持久化数据的部署是无谓重抓。

**本次目标**：

1. 把 `BackgroundRefresher` 改造成路由无关的通用调度器；新增路由不再需要修改 refresher。
2. 提供"预热只跑一次"的可控机制：默认行为基于 `article_store` 是否已有数据自动跳过；同时提供路由级配置开关 `preheat_on_startup` 强制控制。
3. 顺手清理"路由级 TTL 实际未生效（app.py 只读 afdian 的 TTL）"的耦合点。

## 模块边界与职责

| 模块 | 职责 |
|------|------|
| `BackgroundRefresher` | 路由无关的调度器：遍历 enabled 路由 → 决定是否预热 → 维护周期刷新循环。**不读任何 `routes.<具体名>.*` 字段**。 |
| `Route` 基类 | 暴露元数据 `feed_id_field: str = "user_id"`。子路由按需覆写。`fetch()` 接口不变。 |
| `SqliteArticleStore` | 新增 `has_articles(route_name) -> bool`，用于"已有数据则跳过预热"判定（路由粒度）。 |
| `app.py` startup | 改为遍历所有 enabled 路由创建/启动 refresher 任务，而不是只看 afdian。缓存 TTL 改读全局 `cache:` 段。 |
| `_preinit_curl_cffi` | 保留，URL 改为 `refresher.preinit_url`，默认 `https://cn.bing.com/`。留空则跳过。进程级幂等（在 `start()` 顶层调一次）。 |

**关键不变量**：refresher 不会硬编码任何路由名/字段名/URL；新增路由不需要动 refresher 一行代码 —— 只需在 `config.yml` 添加 `routes.<name>` 段即可。

## 配置结构

新版 `config.yml` 结构：

```yaml
server:
  host: "0.0.0.0"
  port: 8000

storage:
  sqlite_path: "./data/rssgen.db"

# 全局缓存 TTL（提到顶级，不再在路由级配置）
cache:
  feed_ttl: 21600
  article_ttl: 43200

scraper:
  proxy: null
  rate_limit: 1.0
  impersonate: "chrome131"

refresher:
  startup_delay: 5
  max_retries: 3
  retry_base_delay: 5
  preinit_url: "https://cn.bing.com/"   # null/空字符串则跳过预初始化

routes:
  afdian:
    enabled: true
    cookie: "..."
    preheat_on_startup: false       # 默认 false，需显式打开；首次部署完成后保持 false
    refresh_interval: 14400         # 缺省/0/null 则关闭定时刷新（仅靠 trigger 兜底）
    feeds:
      - user_id: "q9adg"            # 统一字段名（原 afdian 的 slug 迁移）
        alias: "作者别名"
        limit: 20

  zhihu:
    enabled: true
    cookie: "..."
    preheat_on_startup: false
    refresh_interval: 14400
    feeds:
      - user_id: "kvxjr369f"
        alias: "用户别名"
        limit: 20
```

**变更摘要**：

1. **新增 `cache:` 段**：`feed_ttl` / `article_ttl` 提到全局，所有路由共用一份 TTL。
2. **新增 `refresher.preinit_url`**：取代硬编码 afdian.com。
3. **新增路由级 `preheat_on_startup`**（bool，默认 `false`）：用户需显式打开预热。
4. **`refresh_interval` 提到路由级**：每路由独立。缺省/0/null = 关闭定时刷新。
5. **feeds 字段统一为 `user_id`**：afdian 现有 `slug:` 配置需迁移为 `user_id:`，路由内部仍可用 `slug` 作 URL path 标识（仅配置文件命名变了）。
6. **删除路由级 `feed_ttl` / `article_ttl`**：移到全局 `cache:` 段。

**向后兼容**：不做。一次性破坏性配置迁移，由用户手动改 `config.yml`，`config.example.yml` 同步更新。

**Why**：项目处于早期，无外部用户，引入兼容层是 YAGNI。

## 调度语义（控制流）

每个 enabled 路由各起一个独立 `asyncio.Task` 跑 `_run_route_loop`：

```
[refresher.start()]
  └─ 进程级 _preinit_curl_cffi（仅一次）
  └─ 为每个 enabled 路由创建独立 _run_route_loop task

[_run_route_loop(route_name)]
  ↓ 等待 startup_delay 秒
  ↓
  [预热判定]
    ├─ preheat_on_startup=false → 跳过预热
    ├─ preheat_on_startup=true 且 article_store.has_articles(route_name)=True → 跳过预热（日志：路由已有数据）
    └─ preheat_on_startup=true 且 article_store.has_articles(route_name)=False → 对所有 feed 调用 _refresh_one
  ↓
  [周期刷新]
    ├─ refresh_interval 缺省/0/null → 任务退出（仅靠 trigger 兜底）
    └─ refresh_interval > 0 → while True: sleep(interval) → 刷新所有 feeds
```

**关键设计点**：

1. **预热"已有数据"判定按路由粒度**：现有 schema 限制（无 feed_id 列），路由有任意文章即跳过预热。用户加新 feed 想强制预热 → 改 `preheat_on_startup=true` 重启即可。
2. **预初始化进程级幂等**：在 `BackgroundRefresher.start()` 顶层调用一次，路由 task 内不再调用。无需 Lock/Event，顺序代码即可。
3. **每路由一个 asyncio.Task**：互不阻塞。afdian 失败重试不会拖累 zhihu。
4. **trigger() 路径泛化**：保留现有动态触发逻辑，泛化到所有路由。`_find_feed_config` 内部按 `route_cls.feed_id_field` 查询。
5. **`get_status()` 按路由聚合**：返回 `{route_name: {feed_id: {last_success, error, item_count}}}`，便于 `/status` 接口区分各路由各 feed 状态。

**边界情况**：

- 路由 `enabled=true` 但 `feeds=[]` 或缺省：不创建调度 task（避免空转），但 trigger 路径仍可用。
- 路由 `enabled=false`：完全跳过，不创建任何任务。
- `feed_id_field` 在 `feed_conf` 中找不到对应字段：日志告警 + 跳过该 feed，不抛异常（防止一条坏配置阻断整个路由）。

## 代码改动清单

### `RSSGen/core/route.py`
- 在 `Route` 基类新增类属性 `feed_id_field: str = "user_id"`
- 现有 `AfdianRoute` / `ZhihuRoute` 不需要覆写（直接用默认值）

### `RSSGen/core/article_store.py`
- 新增 `async def has_articles(self, route_name: str) -> bool`
- 实现：`SELECT 1 FROM articles WHERE route=? LIMIT 1`
- 粒度说明：当前 `articles` 表 schema 是 `(route, item_id, content, fetched_at)`，无 `feed_id` 列；故跳过判定按"路由粒度"实现（该路由有任意数据则跳过整个路由的预热）。feed 粒度跳过需要 schema 扩展，超出本次范围，YAGNI 不做。

### `RSSGen/core/refresher.py`（核心改造）
- `__init__`：不再读 `routes.afdian.*`；保存 `self.preinit_url`、`self.config`
- `start()`：先做一次 `_preinit_curl_cffi`，再为每个 enabled 路由创建独立 `_run_route_loop` task；维护 `self._tasks: dict[str, asyncio.Task]`
- `stop()`：取消所有路由 task
- 新增 `async def _run_route_loop(self, route_name: str)`：单路由生命周期
- `_preinit_curl_cffi`：URL 改为 `self.preinit_url`，None/空字符串则跳过（仅日志记录）
- `_refresh_feeds(label, route_name)`：泛化为按路由名读 feeds；`feed_id` 从 `feed_conf[route_cls.feed_id_field]` 取；`fetch_kwargs` 从 feed_conf 中除 `feed_id_field`/`alias` 之外的字段透传
- `_refresh_one`：保持现状（已经是路由无关的）
- `trigger()`：保持现状；`_find_feed_config` 按 `feed_id_field` 查询
- `get_status()`：返回结构改为按路由分组（影响 `/status` 接口输出，需同步前端文档）

### `RSSGen/app.py`
- `feed_cache` / `article_cache` TTL：改为读 `config["cache"]["feed_ttl"]` / `["article_ttl"]`
- startup 中 `if afdian_config.get("enabled", False)` 判断改为 `if any(r.get("enabled") for r in config.get("routes", {}).values())`
- `/feed/{...}` 路径处理逻辑不变

### `config.example.yml` / `config.yml`
- 新增 `cache:` 段
- 新增 `refresher.preinit_url`
- 路由配置：新增 `preheat_on_startup`，删除路由级 `feed_ttl`/`article_ttl`
- afdian feeds：`slug:` → `user_id:`

### `tests/`
- 新增 `tests/test_refresher_decoupling.py`：见下节"测试策略"
- 现有测试：扫描引用了旧字段（如 `feeds[0]["slug"]`、`routes.afdian.refresh_interval`）的，同步迁移

## 测试策略

### 新增 `tests/test_refresher_decoupling.py`

| # | 场景 | 验证点 |
|---|------|--------|
| 1 | 多路由独立调度 | 配置两个 enabled 路由 → `start()` 后存在两个独立 task；其中一个抛异常不影响另一个 |
| 2 | 预热按路由粒度跳过 | mock `article_store.has_articles(route)`：返回 True → 整个路由跳过预热；返回 False → 所有 feed 都预热 |
| 3 | `preheat_on_startup=false` | 无论 article_store 是否有数据，预热阶段都跳过；`_refresh_one` 不被调用 |
| 4 | `refresh_interval` 缺省/0/null | 路由 task 在预热阶段后退出；循环未启动（用 `task.done()` 验证）|
| 5 | `feed_id_field` 取值 | 自定义 Route 子类设 `feed_id_field="custom_key"`：refresher 从 feed_conf 读 `custom_key` 作为 path_params |
| 6 | `fetch_kwargs` 透传 | feed_conf 里除 `user_id`/`alias` 外的字段（如 `limit`、自定义参数）原样传给 `route.fetch()` |
| 7 | `preinit_url=None` | 跳过 `_preinit_curl_cffi`，不发请求 |
| 8 | preinit 进程级幂等 | 启动多个路由时，`_preinit_curl_cffi` 只调用 1 次（mock 调用计数验证）|

### 现有测试调整

- `tests/test_zhihu_route.py` / 其他抓取测试：若不涉及 refresher 集成则不受影响
- 任何引用 `slug` 字段或 `routes.afdian.refresh_interval` 等旧路径的，同步迁移

### 手动验证清单（部署 checklist）

1. 关掉预热：`preheat_on_startup: false` + `refresh_interval: null` → 启动后 trigger 现场触发能正常返回 feed
2. 开预热但已有数据：article_store 中 afdian 路由有任意文章 → 启动日志显示"afdian 路由已有数据，跳过预热"，不重抓
3. 多路由：afdian 关、zhihu 开 → 启动后只有 zhihu 跑预热和定时刷新
4. preinit_url 改成不可达地址 → 启动有 warning 但不阻塞预热

### 不写测试的部分

- curl_cffi 真实请求（mock 掉）
- 配置 schema 校验（项目目前用 `dict.get` 软读取，未引入 pydantic schema，本次不扩范围）

## 实施顺序建议

供 writing-plans 拆分参考：

1. `Route.feed_id_field` 类属性 + `SqliteArticleStore.has_articles`（独立、无依赖）
2. `BackgroundRefresher` 内部重构（按路由分 task、preinit 提到 start、_refresh_feeds 泛化）
3. `app.py` startup 调整（缓存 TTL 来源、enabled 路由聚合判断）
4. 配置文件同步（`config.example.yml` + 用户的 `config.yml`）
5. 测试：新增 + 现有迁移
6. 手动验证清单跑一遍
