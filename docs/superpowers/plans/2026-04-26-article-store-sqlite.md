# 文章缓存持久化（SQLite ArticleStore）实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 afdian 路由的文章正文缓存从内存 `TTLCache` 升级为 SQLite 持久化存储，重启服务后已下载的条目仍可命中。

**Architecture:** 新增 `core/article_store.py`（`SqliteArticleStore` 用 aiosqlite 异步驱动，永不过期，单连接 + asyncio.Lock 串行化），在 `core/cache.py` 新增 `ArticleStoreProtocol` 形式化契约。afdian 路由对存储介质无知，只用 `(route, item_id)` 取/存条目。

**Tech Stack:** Python 3.12 / FastAPI / aiosqlite（已在 pyproject.toml）/ pytest + pytest-asyncio

**Spec:** `docs/superpowers/specs/2026-04-26-article-store-sqlite-design.md`

---

## File Map

| 路径 | 动作 | 责任 |
|---|---|---|
| `RSSGen/core/cache.py` | 修改 | 新增 `ArticleStoreProtocol`，原 `Cache` 类不动 |
| `RSSGen/core/article_store.py` | 新建 | `SqliteArticleStore` 实现 |
| `RSSGen/core/route.py` | 修改 | `Route.fetch` 基类签名加 `article_store` 参数 |
| `RSSGen/routes/afdian.py` | 修改 | `fetch` 改用 `article_store`，删除 key 字符串拼接 |
| `RSSGen/core/refresher.py` | 修改 | 构造参数 `article_cache` → `article_store`，传给 `route.fetch` |
| `RSSGen/app.py` | 修改 | 启动时创建 `SqliteArticleStore`，shutdown 关闭，注入 refresher 与 fallback 路径 |
| `config.example.yml` | 修改 | 新增 `storage.sqlite_path` |
| `docker-compose.yml` | 修改 | rssgen 服务挂载 `./data:/app/data` |
| `tests/test_article_store.py` | 新建 | SqliteArticleStore 单元测试 |
| `tests/test_afdian_caching.py` | 修改 | 改用 `article_store` 参数 |
| `tests/test_refresher.py` | 修改 | 构造器参数重命名（保持测试结构） |

---

## Task 1: 验证依赖与基线测试通过

**Files:** `pyproject.toml`（仅查阅，已含 aiosqlite>=0.22.1）

- [ ] **Step 1: 同步依赖**

```bash
uv sync
```

期望：无新依赖被装，环境就绪。

- [ ] **Step 2: 跑全量测试确认基线绿**

```bash
uv run pytest -q
```

期望：所有现有测试通过（`test_afdian_caching.py` 和 `test_refresher.py`）。如果有失败，先修复或记录已知失败再继续。

---

## Task 2: 在 `cache.py` 新增 `ArticleStoreProtocol`

**Files:**
- Modify: `RSSGen/core/cache.py`

无需测试（Protocol 是类型契约，无运行时行为）。

- [ ] **Step 1: 修改 `RSSGen/core/cache.py`**

替换整个文件内容为：

```python
"""缓存层：内存 TTL 缓存 + 文章存储契约"""

from typing import Protocol

from cachetools import TTLCache


class Cache:
    def __init__(self, maxsize: int = 256, ttl: int = 1800):
        self._cache = TTLCache(maxsize=maxsize, ttl=ttl)

    async def get(self, key: str) -> str | None:
        return self._cache.get(key)

    async def set(self, key: str, value: str):
        self._cache[key] = value


class ArticleStoreProtocol(Protocol):
    """文章持久化存储的契约。

    实现可以是 SQLite、内存、Redis 等任意后端。路由层只通过此接口
    访问文章正文，不关心实际存储介质。
    """

    async def get(self, route: str, item_id: str) -> str | None:
        ...

    async def save(self, route: str, item_id: str, content: str) -> None:
        ...
```

- [ ] **Step 2: 确认导入未报错**

```bash
uv run python -c "from RSSGen.core.cache import Cache, ArticleStoreProtocol; print('ok')"
```

期望：输出 `ok`。

- [ ] **Step 3: 提交**

```bash
git add RSSGen/core/cache.py
git commit -m "feat: 在 cache 模块新增 ArticleStoreProtocol 契约"
```

---

## Task 3: 实现 `SqliteArticleStore`（TDD）

**Files:**
- Create: `RSSGen/core/article_store.py`
- Test: `tests/test_article_store.py`

- [ ] **Step 1: 写失败测试**

创建 `tests/test_article_store.py`：

```python
"""SqliteArticleStore 单元测试"""

import pytest
import pytest_asyncio

from RSSGen.core.article_store import SqliteArticleStore


@pytest_asyncio.fixture
async def store(tmp_path):
    s = SqliteArticleStore(tmp_path / "test.db")
    await s.init()
    yield s
    await s.close()


class TestInit:
    @pytest.mark.asyncio
    async def test_creates_missing_directory(self, tmp_path):
        """init 自动创建不存在的父目录"""
        nested = tmp_path / "nested" / "deeper" / "test.db"
        s = SqliteArticleStore(nested)
        await s.init()
        try:
            assert nested.parent.is_dir()
            assert nested.exists()
        finally:
            await s.close()


class TestRoundTrip:
    @pytest.mark.asyncio
    async def test_save_then_get(self, store):
        """save 后 get 能取回原内容"""
        await store.save("afdian", "post1", "<p>hello</p>")
        result = await store.get("afdian", "post1")
        assert result == "<p>hello</p>"

    @pytest.mark.asyncio
    async def test_unicode_and_html(self, store):
        """支持 Unicode、换行、HTML 标签"""
        content = "<div>中文 内容\n含<br/>换行 特殊字符</div>"
        await store.save("afdian", "post2", content)
        assert await store.get("afdian", "post2") == content

    @pytest.mark.asyncio
    async def test_get_miss_returns_none(self, store):
        """未命中返回 None"""
        assert await store.get("afdian", "nonexistent") is None


class TestKeySemantics:
    @pytest.mark.asyncio
    async def test_replace_on_duplicate(self, store):
        """同 (route, item_id) 重复 save 应覆盖旧值"""
        await store.save("afdian", "post1", "v1")
        await store.save("afdian", "post1", "v2")
        assert await store.get("afdian", "post1") == "v2"

    @pytest.mark.asyncio
    async def test_different_routes_isolated(self, store):
        """同 item_id 不同 route 互不干扰"""
        await store.save("afdian", "x", "afdian-content")
        await store.save("zhihu", "x", "zhihu-content")
        assert await store.get("afdian", "x") == "afdian-content"
        assert await store.get("zhihu", "x") == "zhihu-content"


class TestPersistence:
    @pytest.mark.asyncio
    async def test_data_survives_close_and_reopen(self, tmp_path):
        """close 后用同路径 init，之前 save 的内容仍能取回（核心需求）"""
        path = tmp_path / "persist.db"

        s1 = SqliteArticleStore(path)
        await s1.init()
        await s1.save("afdian", "post1", "<p>persistent</p>")
        await s1.close()

        s2 = SqliteArticleStore(path)
        await s2.init()
        try:
            assert await s2.get("afdian", "post1") == "<p>persistent</p>"
        finally:
            await s2.close()


class TestDegradation:
    @pytest.mark.asyncio
    async def test_get_returns_none_when_uninitialized(self, tmp_path):
        """未 init 直接 get 不抛异常，返回 None"""
        s = SqliteArticleStore(tmp_path / "x.db")
        result = await s.get("afdian", "post1")
        assert result is None

    @pytest.mark.asyncio
    async def test_save_silent_when_uninitialized(self, tmp_path):
        """未 init 直接 save 不抛异常"""
        s = SqliteArticleStore(tmp_path / "x.db")
        await s.save("afdian", "post1", "content")  # 不应抛

    @pytest.mark.asyncio
    async def test_get_after_close_returns_none(self, tmp_path):
        """close 后 get 不抛异常，返回 None"""
        s = SqliteArticleStore(tmp_path / "x.db")
        await s.init()
        await s.close()
        assert await s.get("afdian", "post1") is None

    @pytest.mark.asyncio
    async def test_save_after_close_silent(self, tmp_path):
        """close 后 save 不抛异常"""
        s = SqliteArticleStore(tmp_path / "x.db")
        await s.init()
        await s.close()
        await s.save("afdian", "post1", "content")  # 不应抛
```

注意：`tests/__init__.py` 已存在，新文件无需额外配置。

- [ ] **Step 2: 跑测试确认失败**

```bash
uv run pytest tests/test_article_store.py -v
```

期望：全部失败，错误为 `ModuleNotFoundError: No module named 'RSSGen.core.article_store'`。

- [ ] **Step 3: 实现 `SqliteArticleStore`**

创建 `RSSGen/core/article_store.py`：

```python
"""SQLite 后端的文章持久化存储"""

from __future__ import annotations

import asyncio
import time
from pathlib import Path
from typing import TYPE_CHECKING

import aiosqlite
from loguru import logger

if TYPE_CHECKING:
    from aiosqlite import Connection


_CREATE_TABLE_SQL = """
CREATE TABLE IF NOT EXISTS articles (
    route      TEXT NOT NULL,
    item_id    TEXT NOT NULL,
    content    TEXT NOT NULL,
    fetched_at INTEGER NOT NULL,
    PRIMARY KEY (route, item_id)
) WITHOUT ROWID;
"""

_PRAGMAS = (
    "PRAGMA journal_mode = WAL;",
    "PRAGMA synchronous = NORMAL;",
    "PRAGMA foreign_keys = ON;",
)


class SqliteArticleStore:
    """SQLite 单文件存储，aiosqlite 异步驱动。

    生命周期由调用方管理：startup 调 init()，shutdown 调 close()。
    所有读写串行化（单连接 + asyncio.Lock），并发量本就极小。
    运行期错误降级为 warning + 当作 cache miss；init 失败硬抛。
    """

    def __init__(self, db_path: str | Path):
        self._db_path = Path(db_path)
        self._conn: Connection | None = None
        self._lock = asyncio.Lock()

    async def init(self) -> None:
        self._db_path.parent.mkdir(parents=True, exist_ok=True)
        self._conn = await aiosqlite.connect(self._db_path)
        for pragma in _PRAGMAS:
            await self._conn.execute(pragma)
        await self._conn.execute(_CREATE_TABLE_SQL)
        await self._conn.commit()
        logger.info(f"SqliteArticleStore 初始化完成: {self._db_path}")

    async def close(self) -> None:
        if self._conn is not None:
            await self._conn.close()
            self._conn = None

    async def get(self, route: str, item_id: str) -> str | None:
        if self._conn is None:
            return None
        try:
            async with self._lock:
                async with self._conn.execute(
                    "SELECT content FROM articles WHERE route = ? AND item_id = ?",
                    (route, item_id),
                ) as cursor:
                    row = await cursor.fetchone()
            return row[0] if row else None
        except Exception as e:
            logger.warning(f"ArticleStore.get 失败 ({route}/{item_id}): {e}")
            return None

    async def save(self, route: str, item_id: str, content: str) -> None:
        if self._conn is None:
            return
        try:
            now = int(time.time())
            async with self._lock:
                await self._conn.execute(
                    "INSERT OR REPLACE INTO articles "
                    "(route, item_id, content, fetched_at) VALUES (?, ?, ?, ?)",
                    (route, item_id, content, now),
                )
                await self._conn.commit()
        except Exception as e:
            logger.warning(f"ArticleStore.save 失败 ({route}/{item_id}): {e}")
```

- [ ] **Step 4: 跑测试确认通过**

```bash
uv run pytest tests/test_article_store.py -v
```

期望：全部 pass（共 11 个测试）。

- [ ] **Step 5: 提交**

```bash
git add RSSGen/core/article_store.py tests/test_article_store.py
git commit -m "feat: 实现 SqliteArticleStore 文章持久化存储"
```

---

## Task 4: 扩展 `Route` 基类签名加 `article_store`

**Files:**
- Modify: `RSSGen/core/route.py`

无需独立测试（仅签名扩展，行为由子类测试覆盖）。

- [ ] **Step 1: 修改 `RSSGen/core/route.py:37-43`**

把：

```python
    async def fetch(self, article_cache=None, **kwargs) -> list[FeedItem]:
        """抓取数据源。

        参数:
            article_cache: 可选的文章级缓存实例，子类可利用它跳过已缓存文章的 API 调用
        """
        raise NotImplementedError
```

改为：

```python
    async def fetch(self, article_cache=None, article_store=None, **kwargs) -> list[FeedItem]:
        """抓取数据源。

        参数:
            article_cache: 可选的内存型缓存（key-value），适合临时性数据源
            article_store: 可选的持久化存储（ArticleStoreProtocol），适合需要跨重启
                保留已抓内容的数据源
        """
        raise NotImplementedError
```

- [ ] **Step 2: 跑现有测试确认未破坏**

```bash
uv run pytest tests/ -v
```

期望：全部通过（afdian 现在的实现仍然只用 `article_cache`，新增的 kwargs 被忽略）。

- [ ] **Step 3: 提交**

```bash
git add RSSGen/core/route.py
git commit -m "feat: Route 基类 fetch 新增 article_store 参数"
```

---

## Task 5: 重写 `tests/test_afdian_caching.py` 切换到 `article_store`（TDD 失败侧）

**Files:**
- Modify: `tests/test_afdian_caching.py`

- [ ] **Step 1: 用新版本完全替换**

把 `tests/test_afdian_caching.py` 替换为：

```python
"""爱发电路由文章级持久化逻辑测试"""

import pytest
import pytest_asyncio
from unittest.mock import AsyncMock, patch

from RSSGen.core.article_store import SqliteArticleStore
from RSSGen.routes.afdian import AfdianRoute


@pytest.fixture
def route():
    return AfdianRoute({"cookie": "test", "rate_limit": 0})


@pytest_asyncio.fixture
async def article_store(tmp_path):
    s = SqliteArticleStore(tmp_path / "afdian.db")
    await s.init()
    yield s
    await s.close()


def _make_post(post_id: str):
    return {
        "post_id": post_id,
        "title": "test",
        "publish_time": 1700000000,
        "pics": [],
        "user": {"name": "a"},
    }


class TestFetchWithStore:
    @pytest.mark.asyncio
    async def test_store_hit_skips_api(self, route, article_store):
        """store 命中时不调用详情 API"""
        await article_store.save("afdian", "post1", "<p>cached content</p>")
        mock_posts = [_make_post("post1")]

        with patch.object(route, "_get_author_id", new_callable=AsyncMock, return_value="uid1"), \
             patch.object(route, "_get_post_list", new_callable=AsyncMock, return_value=mock_posts), \
             patch.object(route, "_get_post_detail", new_callable=AsyncMock) as mock_detail:

            items = await route.fetch(article_store=article_store, path_params=["slug1"])

            mock_detail.assert_not_called()
            assert len(items) == 1
            assert items[0].content == "<p>cached content</p>"

    @pytest.mark.asyncio
    async def test_store_miss_calls_api_and_saves(self, route, article_store):
        """store 未命中时调用 API 并落库"""
        mock_posts = [_make_post("post2")]

        with patch.object(route, "_get_author_id", new_callable=AsyncMock, return_value="uid1"), \
             patch.object(route, "_get_post_list", new_callable=AsyncMock, return_value=mock_posts), \
             patch.object(route, "_get_post_detail", new_callable=AsyncMock, return_value="<p>fresh</p>"):

            items = await route.fetch(article_store=article_store, path_params=["slug1"])

            assert items[0].content == "<p>fresh</p>"
            saved = await article_store.get("afdian", "post2")
            assert saved == "<p>fresh</p>"

    @pytest.mark.asyncio
    async def test_no_store_still_works(self, route):
        """不传 article_store 时仍能正常走 API"""
        mock_posts = [_make_post("post3")]

        with patch.object(route, "_get_author_id", new_callable=AsyncMock, return_value="uid1"), \
             patch.object(route, "_get_post_list", new_callable=AsyncMock, return_value=mock_posts), \
             patch.object(route, "_get_post_detail", new_callable=AsyncMock, return_value="<p>detail</p>"):

            items = await route.fetch(path_params=["slug1"])

            assert items[0].content == "<p>detail</p>"
```

- [ ] **Step 2: 跑测试确认失败**

```bash
uv run pytest tests/test_afdian_caching.py -v
```

期望：`test_store_hit_skips_api` 失败（因为 afdian 还没改，传入的 `article_store` 被忽略，仍会调 `_get_post_detail`），其余两个可能巧合通过但语义不对。这是 TDD 的红灯阶段。

---

## Task 6: 改造 `routes/afdian.py` 使用 `article_store`（TDD 绿灯）

**Files:**
- Modify: `RSSGen/routes/afdian.py:117-169`

- [ ] **Step 1: 替换 `fetch` 方法**

把 `RSSGen/routes/afdian.py` 中的 `async def fetch(...)` 整个方法（约 117-169 行）替换为：

```python
    async def fetch(self, article_store=None, **kwargs) -> list[FeedItem]:
        path_params: list[str] = kwargs.get("path_params", [])
        if not path_params:
            raise ValueError("需要指定作者 url_slug，如 /feed/afdian/{author_slug}")
        author_slug = path_params[0]

        limit = int(kwargs.get("limit", 20))
        logger.info(f"开始抓取 {author_slug}，limit={limit}")

        scraper = self._get_scraper()
        user_id = await self._get_author_id(scraper, author_slug)
        posts = await self._get_post_list(scraper, user_id, author_slug, limit=limit)

        items = []
        for post in posts:
            publish_time = int(post.get("publish_time", 0))
            pub_date = datetime.fromtimestamp(publish_time, tz=timezone.utc) if publish_time else None

            enclosures = []
            for pic in post.get("pics", []):
                if pic:
                    enclosures.append({"url": pic, "type": "image/jpeg"})

            post_id = post.get("post_id", "")

            content = None
            if article_store:
                content = await article_store.get("afdian", post_id)

            if content is None:
                content = await self._get_post_detail(scraper, post_id)
                logger.info(f"文章详情下载成功: {post.get('title', post_id)}")
                if article_store and content:
                    await article_store.save("afdian", post_id, content)

            items.append(FeedItem(
                title=post.get("title", "无标题"),
                link=f"{HOST_URL}/p/{post_id}",
                content=content or "",
                pub_date=pub_date,
                author=post.get("user", {}).get("name"),
                guid=post_id,
                enclosures=enclosures,
            ))

        logger.info(f"抓取完成 {author_slug}: {len(items)} 条文章")
        return items
```

注意：参数列表里删除了 `article_cache=None`（基类仍保留，但 afdian 不再接受），并把 `**kwargs` 中可能传来的 `article_cache` 静默吞掉（如果有外部代码还在传，会被 `**kwargs` 接住而不报错）。

- [ ] **Step 2: 跑 afdian 测试确认通过**

```bash
uv run pytest tests/test_afdian_caching.py -v
```

期望：3 个测试全部通过。

- [ ] **Step 3: 跑全量测试**

```bash
uv run pytest -q
```

期望：除 `test_refresher.py` 仍按旧参数名构造（参数名改前还能跑），全部通过。

- [ ] **Step 4: 提交**

```bash
git add RSSGen/routes/afdian.py tests/test_afdian_caching.py
git commit -m "refactor: afdian 路由改用 article_store 持久化文章正文"
```

---

## Task 7: 更新 `BackgroundRefresher` 参数名与调用

**Files:**
- Modify: `RSSGen/core/refresher.py:20-22`, `:166`
- Modify: `tests/test_refresher.py`

- [ ] **Step 1: 改 refresher 构造参数与调用**

修改 `RSSGen/core/refresher.py`：

把第 20-23 行：

```python
    def __init__(self, feed_cache: Cache, article_cache: Cache, config: dict):
        self.feed_cache = feed_cache
        self.article_cache = article_cache
        self.config = config
```

改为：

```python
    def __init__(self, feed_cache: Cache, article_store, config: dict):
        self.feed_cache = feed_cache
        self.article_store = article_store
        self.config = config
```

把第 166 行：

```python
                    items = await route.fetch(article_cache=self.article_cache, **kwargs)
```

改为：

```python
                    items = await route.fetch(article_store=self.article_store, **kwargs)
```

注意 `article_store` 不强类型标注，因为 Python typing 跟 Protocol 互动有时绕，保持简洁。

- [ ] **Step 2: 同步更新 `tests/test_refresher.py`**

把 `tests/test_refresher.py` 中所有 `article_cache` 标识符（fixture 名、变量名、构造参数）替换为 `article_store`。具体：

修改第 28-32 行 fixture：

```python
@pytest.fixture
def caches():
    feed_cache = Cache(ttl=60)
    article_store = Cache(ttl=60)  # 测试中用 Cache 充当 store；不调用其 get/save
    return feed_cache, article_store
```

并把所有 `feed_cache, article_cache = caches` 解包改为 `feed_cache, article_store = caches`，构造调用 `BackgroundRefresher(feed_cache, article_store, ...)`。

注意：`Cache` 没有 `(route, item_id)` 形式的 `get/save`，但测试里 `_refresh_one` 要么被 mock，要么 `route.fetch` 被 mock，**`article_store` 实际没人调用其 get/save**，所以 `Cache` 占位足够。

涉及行（搜索 `article_cache` 全部替换）：
- 第 31 行（fixture 内变量）
- 第 48, 60, 75, 96, 113 行（解包）
- 第 49, 61, 76, 97, 114 行（构造器第二参数）

- [ ] **Step 3: 跑全量测试**

```bash
uv run pytest -q
```

期望：全部通过。

- [ ] **Step 4: 提交**

```bash
git add RSSGen/core/refresher.py tests/test_refresher.py
git commit -m "refactor: BackgroundRefresher 参数 article_cache 重命名为 article_store"
```

---

## Task 8: `app.py` 装配 `SqliteArticleStore`

**Files:**
- Modify: `RSSGen/app.py`

- [ ] **Step 1: 改造 startup/shutdown 与 fallback 路径**

把 `RSSGen/app.py` 中 import 段（第 1-13 行附近）增加：

```python
from RSSGen.core.article_store import SqliteArticleStore
```

把第 25-28 行的全局变量声明：

```python
feed_cache: Cache | None = None
article_cache: Cache | None = None
refresher: BackgroundRefresher | None = None
config: dict = {}
```

改为：

```python
feed_cache: Cache | None = None
article_cache: Cache | None = None
article_store: SqliteArticleStore | None = None
refresher: BackgroundRefresher | None = None
config: dict = {}
```

把第 31-44 行 startup 函数：

```python
@app.on_event("startup")
async def startup():
    global config, feed_cache, article_cache, refresher
    config = load_config()
    discover_routes()
    logger.info(f"已加载路由: {list(get_registry().keys())}")

    afdian_config = config.get("routes", {}).get("afdian", {})
    feed_cache = Cache(ttl=afdian_config.get("feed_ttl", 21600))
    article_cache = Cache(ttl=afdian_config.get("article_ttl", 43200))

    if afdian_config.get("enabled", False):
        refresher = BackgroundRefresher(feed_cache, article_cache, config)
        await refresher.start()
```

替换为：

```python
@app.on_event("startup")
async def startup():
    global config, feed_cache, article_cache, article_store, refresher
    config = load_config()
    discover_routes()
    logger.info(f"已加载路由: {list(get_registry().keys())}")

    afdian_config = config.get("routes", {}).get("afdian", {})
    feed_cache = Cache(ttl=afdian_config.get("feed_ttl", 21600))
    # 保留：通用内存型 KV，不再接入 afdian，留给将来其他路由 / 测试使用
    article_cache = Cache(ttl=afdian_config.get("article_ttl", 43200))

    sqlite_path = config.get("storage", {}).get("sqlite_path", "./data/rssgen.db")
    article_store = SqliteArticleStore(sqlite_path)
    await article_store.init()

    if afdian_config.get("enabled", False):
        refresher = BackgroundRefresher(feed_cache, article_store, config)
        await refresher.start()
```

把第 47-51 行 shutdown 函数：

```python
@app.on_event("shutdown")
async def shutdown():
    global refresher
    if refresher:
        await refresher.stop()
```

替换为：

```python
@app.on_event("shutdown")
async def shutdown():
    global refresher, article_store
    if refresher:
        await refresher.stop()
    if article_store:
        await article_store.close()
```

把第 96 行 fallback 路径：

```python
        items = await route.fetch(article_cache=article_cache, **kwargs)
```

替换为：

```python
        items = await route.fetch(article_store=article_store, **kwargs)
```

- [ ] **Step 2: 静态导入检查**

```bash
uv run python -c "from RSSGen.app import app; print('ok')"
```

期望：输出 `ok`，无 ImportError。

- [ ] **Step 3: 跑全量测试**

```bash
uv run pytest -q
```

期望：全部通过（app.py 不被任何测试直接导入 startup 流程，仅静态导入校验）。

- [ ] **Step 4: 提交**

```bash
git add RSSGen/app.py
git commit -m "feat: app 启动时初始化 SqliteArticleStore，shutdown 关闭"
```

---

## Task 9: 更新 `config.example.yml` 与 `docker-compose.yml`

**Files:**
- Modify: `config.example.yml`
- Modify: `docker-compose.yml`

- [ ] **Step 1: `config.example.yml` 顶部新增 `storage` 段**

在第 8 行 `server:` 段之后、`scraper:` 之前插入：

```yaml
# 持久化存储（文章正文持久化到 SQLite，避免重启重抓）
storage:
  sqlite_path: "./data/rssgen.db"   # 文章数据库路径，相对项目根；Docker 内会挂载
```

- [ ] **Step 2: `docker-compose.yml` 第 7-8 行 rssgen 的 volumes 段加一行**

把：

```yaml
    volumes:
      - ./config.yml:/app/config.yml:ro
```

改为：

```yaml
    volumes:
      - ./config.yml:/app/config.yml:ro
      - ./data:/app/data
```

- [ ] **Step 3: 提交**

```bash
git add config.example.yml docker-compose.yml
git commit -m "build: 配置 SQLite 存储路径与 Docker volume 挂载"
```

---

## Task 10: 烟测（手动验证持久化跨重启生效）

**Files:** 无代码改动；仅运行验证。

- [ ] **Step 1: 准备最小可启动的 `config.yml`**

如果项目根没有 `config.yml`，从模板创建（仅本地烟测，**不要提交**）：

```bash
test -f config.yml || cp config.example.yml config.yml
```

如果 `config.yml` 已存在但缺 `storage` 段，手动加上：

```yaml
storage:
  sqlite_path: "./data/rssgen.db"
```

把 `routes.afdian.enabled` 设为 `false`（避免烟测时真的去拉爱发电）：

```yaml
routes:
  afdian:
    enabled: false
    cookie: "dummy"
```

- [ ] **Step 2: 启动服务确认 store 初始化**

```bash
uv run uvicorn RSSGen.app:app --host 127.0.0.1 --port 8000 --no-access-log &
SERVER_PID=$!
sleep 3
```

期望日志包含：`SqliteArticleStore 初始化完成: data/rssgen.db`。

- [ ] **Step 3: 验证 SQLite 文件存在且 schema 正确**

```bash
ls -la data/rssgen.db && \
  uv run python -c "import sqlite3; c=sqlite3.connect('data/rssgen.db'); print(c.execute('SELECT sql FROM sqlite_master WHERE name=\"articles\"').fetchone())"
```

期望：文件存在；输出形如 `('CREATE TABLE articles (route TEXT NOT NULL, item_id TEXT NOT NULL, content TEXT NOT NULL, fetched_at INTEGER NOT NULL, PRIMARY KEY (route, item_id)) WITHOUT ROWID',)`。

- [ ] **Step 4: 写入测试条目，模拟重启验证持久化**

```bash
uv run python -c "
import asyncio
from RSSGen.core.article_store import SqliteArticleStore

async def main():
    s = SqliteArticleStore('data/rssgen.db')
    await s.init()
    await s.save('afdian', 'smoketest', '<p>before restart</p>')
    print('saved:', await s.get('afdian', 'smoketest'))
    await s.close()

asyncio.run(main())
"
```

期望：`saved: <p>before restart</p>`。

- [ ] **Step 5: 关闭服务并重新打开，验证条目仍在**

```bash
kill $SERVER_PID 2>/dev/null
sleep 1

uv run python -c "
import asyncio
from RSSGen.core.article_store import SqliteArticleStore

async def main():
    s = SqliteArticleStore('data/rssgen.db')
    await s.init()
    print('after restart:', await s.get('afdian', 'smoketest'))
    await s.close()

asyncio.run(main())
"
```

期望：`after restart: <p>before restart</p>`——这就是核心需求的端到端验证。

- [ ] **Step 6: 清理烟测残留**

```bash
rm -f data/rssgen.db data/rssgen.db-wal data/rssgen.db-shm
```

`config.yml` 不要提交（已在 .gitignore）。

- [ ] **Step 7: 跑最终全量测试**

```bash
uv run pytest -q
```

期望：全部通过。

- [ ] **Step 8: 确认本任务无新提交**

```bash
git status
```

期望：除了可能的本地 `config.yml` 改动（已 gitignore），无未提交的代码改动。

---

## 完成标准

- [ ] `uv run pytest -q` 全绿
- [ ] 启动 uvicorn 时日志显示 `SqliteArticleStore 初始化完成`
- [ ] `data/rssgen.db` 文件被创建，包含 `articles` 表
- [ ] 重启服务后，先前 `save` 的条目仍可 `get` 到
- [ ] `git log --oneline` 显示约 8 个分阶段提交，每个对应一个 Task
