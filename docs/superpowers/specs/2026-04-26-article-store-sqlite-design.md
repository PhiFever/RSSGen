# 文章缓存持久化（SQLite ArticleStore）设计

**日期**：2026-04-26
**状态**：已确认设计，待实施
**关联**：`RSSGen/core/cache.py`、`RSSGen/core/refresher.py`、`RSSGen/routes/afdian.py`、`RSSGen/app.py`

## 背景与目标

当前 `article_cache` 是基于 cachetools 的内存 `TTLCache`，服务重启后所有文章正文丢失，下一轮预热/请求会触发对每篇文章的 `get-detail` 接口调用，既慢又增加被风控风险。

**目标**：把"已下载的文章正文"持久化到 SQLite，重启后能直接命中已保存的条目，不再重抓。

**非目标**：
- 不持久化 `feed_cache`（XML 是派生数据，重渲染成本极低）
- 不做"已知条目索引 + 列表翻页早停"优化（当前不是瓶颈，YAGNI）
- 不引入双层（内存 + SQLite）缓存（项目规模不需要）

## 关键决策

| 决策 | 选择 | 理由 |
|---|---|---|
| 持久化范围 | 仅 `article_cache`（条目正文） | feed_cache 是派生数据，价值低 |
| TTL 策略 | 永不过期 | 爱发电文章基本不可变；语义最干净 |
| 模块组织 | 独立模块 `core/article_store.py` | 语义清晰，与通用 Cache 解耦 |
| 接口抽象 | `typing.Protocol` 显式契约 | 多实现可替换（生产 SQLite / 测试可用内存） |
| Schema | 含 `fetched_at` 时间戳 | 极小成本，便于运维观测 |
| 文件位置 | 可配置，默认 `./data/rssgen.db` | 配合 Docker volume 挂载 |
| 异步驱动 | `aiosqlite` | 社区事实标准，代码可读性最优 |
| 错误处理 | 运行期降级，初始化期硬失败 | 鲁棒性 vs 配置错误的早暴露 |
| 路由层心智 | 对存储介质无知 | 路由只关心"取/存条目"，实现可换 |

## 架构

### 模块布局

```
RSSGen/core/
  cache.py          # 既有：通用 TTLCache 封装（保留），新增 ArticleStoreProtocol
  article_store.py  # 新增：SqliteArticleStore（aiosqlite 实现）
```

`Cache` 类继续给 `feed_cache` 使用。`app.py` 中的 `article_cache` 实例**保留但当前不再接入任何路由**（afdian 改用 `article_store`）；保留它的目的是：
- 未来加新路由时，如果该路由不需要持久化（比如临时性数据源），可以直接复用此实例
- 在 afdian 之外的测试场景里，方便构造内存型 `ArticleStoreProtocol` 替代品

### 接口契约

```python
# core/cache.py 中新增
from typing import Protocol

class ArticleStoreProtocol(Protocol):
    async def get(self, route: str, item_id: str) -> str | None: ...
    async def save(self, route: str, item_id: str, content: str) -> None: ...
```

参数直接传 `(route, item_id)` 而不是拼好的 key 字符串，让路由层不再持有"缓存键命名约定"这种实现细节。

### 数据流

```
请求到达 → afdian.fetch(article_store=...)
  → 列表接口照常拉取（不变）
  → 每个 post：article_store.get("afdian", post_id)
       命中 → 直接用
       未命中 → _get_post_detail() → article_store.save("afdian", post_id, content)
```

## 数据库设计

### Schema

```sql
CREATE TABLE IF NOT EXISTS articles (
    route      TEXT NOT NULL,
    item_id    TEXT NOT NULL,
    content    TEXT NOT NULL,
    fetched_at INTEGER NOT NULL,         -- Unix 时间戳，秒
    PRIMARY KEY (route, item_id)
) WITHOUT ROWID;
```

- `WITHOUT ROWID`：主键即查询入口，避免一层间接
- 不建额外索引：主键已覆盖唯一查询路径

### PRAGMA（连接建立时）

```sql
PRAGMA journal_mode = WAL;       -- 写不阻塞读
PRAGMA synchronous = NORMAL;     -- 性能/安全平衡
PRAGMA foreign_keys = ON;        -- 习惯性开启
```

### 写策略

`INSERT OR REPLACE`，不是 `INSERT OR IGNORE`——永不过期不代表内容永不变，将来加手动刷新接口或调试时，重 save 同条目应该覆盖。

## `SqliteArticleStore` API

```python
class SqliteArticleStore:
    def __init__(self, db_path: str | Path):
        self._db_path = Path(db_path)
        self._conn: aiosqlite.Connection | None = None
        self._lock = asyncio.Lock()

    async def init(self) -> None:
        """建目录、打开连接、设置 PRAGMA、建表。失败则抛异常（启动期硬失败）。"""

    async def close(self) -> None:
        """关闭连接。"""

    async def get(self, route: str, item_id: str) -> str | None:
        """查询单条文章正文。读失败 → 记 warning，返回 None（按 cache miss 处理）。"""

    async def save(self, route: str, item_id: str, content: str) -> None:
        """落库（INSERT OR REPLACE）。写失败 → 记 warning，吞掉异常。"""
```

### 连接与并发

- 单个长连接（`aiosqlite.Connection`）
- 所有 `get`/`save` 通过 `asyncio.Lock` 串行化（aiosqlite 单连接不能并发执行语句；并发量极低，开销忽略）
- `init` 内部 `self._db_path.parent.mkdir(parents=True, exist_ok=True)`

### 错误处理

| 场景 | 行为 |
|---|---|
| `init()` 失败（路径不可写、磁盘满等） | 异常抛出 → 进程崩 → 配置问题立即暴露 |
| `get()` 运行期 DB 错误 | `logger.warning(...)` + `return None`（当作 miss） |
| `save()` 运行期 DB 错误 | `logger.warning(...)` + 静默返回（不影响本次请求） |

### 不暴露的能力（YAGNI）

- 没有 `delete` / `delete_by_route` / `list_ids` / `exists`——目前没有调用方
- 没有批量接口——`save` 调用频次低（每篇 miss 一次）

## 装配与配置

### `config.example.yml` 新增

```yaml
storage:
  sqlite_path: "./data/rssgen.db"   # 文章持久化存储路径
```

### `app.py` startup 改造

```python
@app.on_event("startup")
async def startup():
    global config, feed_cache, article_cache, article_store, refresher
    config = load_config()
    discover_routes()

    # 既有：保持不变
    feed_cache = Cache(ttl=afdian_config.get("feed_ttl", 21600))
    article_cache = Cache(ttl=afdian_config.get("article_ttl", 43200))  # 见下文说明

    sqlite_path = config.get("storage", {}).get("sqlite_path", "./data/rssgen.db")
    article_store = SqliteArticleStore(sqlite_path)
    await article_store.init()       # 失败 → 进程崩

    if afdian_config.get("enabled", False):
        refresher = BackgroundRefresher(feed_cache, article_store, config)
        await refresher.start()


@app.on_event("shutdown")
async def shutdown():
    if refresher: await refresher.stop()
    if article_store: await article_store.close()
```

### `BackgroundRefresher` 改造

构造参数从 `article_cache: Cache` 改名为 `article_store: ArticleStoreProtocol`，并把它原样传给 `route.fetch(article_store=...)`。

### 同步路径（`/feed/{route}/{path}` 直连分支）

`app.py` 中无 refresher 兜底分支也改为传 `article_store=article_store`。

### `docker-compose.yml`

rssgen 服务新增 volume：

```yaml
volumes:
  - ./data:/app/data
```

## 路由层改动

### `Route` 基类签名

```python
async def fetch(self, article_cache=None, article_store=None, **kwargs) -> list[FeedItem]:
    raise NotImplementedError
```

`article_cache` 保留给将来不需要持久化的路由；`article_store` 给需要持久化的路由（如 afdian）。两参数都默认 `None`，子类按需取用。当前 `app.py` 调用 `fetch` 时只传 `article_store`，不传 `article_cache`——后者要等有具体路由需求时再接线。

### `routes/afdian.py` 改动（仅 `fetch` 中段）

```python
async def fetch(self, article_store=None, **kwargs) -> list[FeedItem]:
    ...
    for post in posts:
        post_id = post.get("post_id", "")

        content = None
        if article_store:
            content = await article_store.get("afdian", post_id)

        if content is None:
            content = await self._get_post_detail(scraper, post_id)
            logger.info(f"文章详情下载成功: {post.get('title', post_id)}")
            if article_store and content:
                await article_store.save("afdian", post_id, content)

        items.append(FeedItem(...))
```

差异：
- 不再拼 `article:afdian:{post_id}` 字符串 key
- `cache_hit` 中间变量删除，`content is None` 即未命中

## 测试策略

### `tests/test_article_store.py`（新建）

用 `tmp_path` fixture 隔离每个测试。覆盖：

- `init()` 在不存在的目录下能自动建目录、建表
- `save` + `get` 往返一致（含 HTML 标签、换行、Unicode）
- 同 `(route, item_id)` 重复 `save` 应覆盖（`INSERT OR REPLACE` 语义）
- 不同 `(route, item_id)` 互不干扰
- `get` 未命中返回 `None`
- **重启场景**：`close()` 后用同路径再 `init()`，之前 `save` 的内容仍能 `get` 到（核心需求验证）
- 降级行为：连接不可用时 `get→None` / `save→静默`，不抛异常

### `tests/test_afdian_caching.py`（改造）

把现有用 `Cache`/`article_cache` 的地方改成构造 `SqliteArticleStore`（`tmp_path`），验证 afdian 路由的 hit/miss 行为不变。

### 不写的测试（YAGNI）

- 性能/压力测试：目标量级（每 slug 几十到几百条）远低于 SQLite 极限
- 并发竞争测试：单连接 + Lock 已从根上排除竞争

## 依赖变更

`pyproject.toml` 新增：

```toml
"aiosqlite>=0.20.0",
```

## 部署影响

- 首次部署：`./data/` 目录会被自动创建，SQLite 文件首次启动时建表
- 升级现有部署：`docker compose up -d` 后会自动挂载 volume，重启即生效
- 回滚：删除 `./data/rssgen.db` 文件即可清空持久化缓存（或改回旧版镜像，新增的代码对老配置无副作用）

## 不在本次范围

- "已知条目索引 + 列表翻页早停"优化（如有需要再开新 spec）
- 手动失效接口（`DELETE /admin/cache/...`）
- 持久化 feed_cache
- Redis 后端（Protocol 已留空间，不实现）
