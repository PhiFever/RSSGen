# 后台调度器解耦与可配置化 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 `BackgroundRefresher` 从 afdian 专用改造成路由无关的通用调度器，新增路由不再需要修改 refresher 代码。

**Architecture:** 每个 enabled 路由各起一个独立 `asyncio.Task` 跑 `_run_route_loop`，预热与定时刷新是同一任务的两个阶段。预热判定支持路由粒度的"已有数据则跳过"（`article_store.has_articles(route_name)`）+ 路由级配置开关 `preheat_on_startup` 双重控制。

**Tech Stack:** Python 3.12, asyncio, aiosqlite, curl_cffi, FastAPI, pytest

---

## File Structure

| File | Action | Responsibility |
|------|--------|---------------|
| `RSSGen/core/route.py` | Modify | 新增 `feed_id_field: str = "user_id"` 类属性 |
| `RSSGen/core/article_store.py` | Modify | 新增 `has_articles(route_name) -> bool` 方法 |
| `RSSGen/core/refresher.py` | Modify | 核心改造：路由无关调度、per-route task、preinit 提到 start |
| `RSSGen/routes/afdian.py` | Modify | `lookup_alias` key 从 `"slug"` 改为 `"user_id"`（配合 config 迁移）|
| `RSSGen/app.py` | Modify | 缓存 TTL 改读全局 `cache:` 段、enabled 路由聚合判断 |
| `config.example.yml` | Modify | 新增 `cache:` 段、`refresher.preinit_url`、`preheat_on_startup`、`slug` → `user_id` |
| `config.yml` | Modify | 同步 config.example.yml 的结构变更 |
| `tests/test_article_store.py` | Modify | 新增 `has_articles` 测试 |
| `tests/test_refresher.py` | Modify | 重写为路由无关的测试 + 新增场景 |
| `tests/conftest.py` | Modify | 更新 fixtures |

---

### Task 1: Route.feed_id_field 类属性

**Files:**
- Modify: `RSSGen/core/route.py:25-31`

- [ ] **Step 1: 在 Route 基类添加 feed_id_field**

在 `Route` 类的 `description` 行之后添加：

```python
class Route:
    """路由基类，每个数据源继承此类"""

    name: str = ""
    description: str = ""
    feed_id_field: str = "user_id"  # feeds 配置中用于标识 feed 的字段名
```

- [ ] **Step 2: 验证现有路由不需要改动**

确认 `AfdianRoute` 和 `ZhihuRoute` 都没有覆写 `feed_id_field`。afdian 现有配置用 `slug`，但 config 迁移后统一为 `user_id`，所以默认值 `"user_id"` 直接适用。

```bash
grep -rn "feed_id_field" RSSGen/routes/
```

预期：无输出（无覆写）

- [ ] **Step 3: Commit**

```bash
git add RSSGen/core/route.py
git commit -m "feat(route): 添加 feed_id_field 类属性"
```

---

### Task 2: SqliteArticleStore.has_articles

**Files:**
- Modify: `RSSGen/core/article_store.py:86-89`
- Modify: `tests/test_article_store.py`

- [ ] **Step 1: 写失败测试**

在 `tests/test_article_store.py` 末尾新增测试类：

```python
class TestHasArticles:
    @pytest.mark.asyncio
    async def test_empty_store_returns_false(self, store):
        """空数据库返回 False"""
        assert await store.has_articles("afdian") is False

    @pytest.mark.asyncio
    async def test_with_data_returns_true(self, store):
        """有数据时返回 True"""
        await store.save("afdian", "post1", "<p>content</p>")
        assert await store.has_articles("afdian") is True

    @pytest.mark.asyncio
    async def test_route_isolation(self, store):
        """只检查指定路由，不被其他路由的数据影响"""
        await store.save("afdian", "post1", "<p>content</p>")
        assert await store.has_articles("zhihu") is False

    @pytest.mark.asyncio
    async def test_uninitialized_returns_false(self, tmp_path):
        """未 init 返回 False（降级）"""
        s = SqliteArticleStore(tmp_path / "x.db")
        assert await s.has_articles("afdian") is False
```

- [ ] **Step 2: 运行测试确认失败**

```bash
cd /mnt/d/MyProject/Python/RSSGen && uv run pytest tests/test_article_store.py::TestHasArticles -v
```

预期：FAIL — `has_articles` 不存在

- [ ] **Step 3: 实现 has_articles 方法**

在 `SqliteArticleStore` 类末尾（`save` 方法之后）添加：

```python
    async def has_articles(self, route: str) -> bool:
        """检查指定路由是否已有文章数据（路由粒度）"""
        if self._conn is None:
            return False
        try:
            async with self._lock:
                async with self._conn.execute(
                    "SELECT 1 FROM articles WHERE route = ? LIMIT 1",
                    (route,),
                ) as cursor:
                    row = await cursor.fetchone()
            return row is not None
        except Exception as e:
            logger.warning(f"ArticleStore.has_articles 失败 ({route}): {e}")
            return False
```

- [ ] **Step 4: 运行测试确认通过**

```bash
cd /mnt/d/MyProject/Python/RSSGen && uv run pytest tests/test_article_store.py::TestHasArticles -v
```

预期：全部 PASS

- [ ] **Step 5: 运行全量 article_store 测试确认无回归**

```bash
cd /mnt/d/MyProject/Python/RSSGen && uv run pytest tests/test_article_store.py -v
```

预期：全部 PASS

- [ ] **Step 6: Commit**

```bash
git add RSSGen/core/article_store.py tests/test_article_store.py
git commit -m "feat(article-store): 添加 has_articles 路由粒度判定"
```

---

### Task 3: BackgroundRefresher 核心重构

这是最大的一个 task。改造 `refresher.py`：从 afdian 硬编码变为路由无关的通用调度器。

**Files:**
- Modify: `RSSGen/core/refresher.py` (全文)

- [ ] **Step 1: 写失败测试 — 多路由独立调度**

在 `tests/test_refresher.py` 中，先更新 fixtures 和基础测试。完整重写 `tests/test_refresher.py`：

```python
"""BackgroundRefresher 路由无关调度器测试"""

import asyncio

import pytest
from unittest.mock import AsyncMock, MagicMock, patch

from RSSGen.core.cache import Cache
from RSSGen.core.refresher import BackgroundRefresher


# ── fixtures ──────────────────────────────────────────────

@pytest.fixture
def multi_route_config():
    """两个 enabled 路由的配置"""
    return {
        "refresher": {
            "startup_delay": 0,
            "max_retries": 1,
            "retry_base_delay": 0,
            "preinit_url": None,
        },
        "scraper": {},
        "routes": {
            "afdian": {
                "enabled": True,
                "cookie": "test",
                "preheat_on_startup": False,
                "refresh_interval": 0,
                "feeds": [
                    {"user_id": "author1", "limit": 5},
                ],
            },
            "zhihu": {
                "enabled": True,
                "cookie": "test",
                "preheat_on_startup": False,
                "refresh_interval": 0,
                "feeds": [
                    {"user_id": "user1", "limit": 10},
                ],
            },
        },
    }


@pytest.fixture
def caches():
    feed_cache = Cache(ttl=60)
    article_store = MagicMock()
    article_store.has_articles = AsyncMock(return_value=False)
    return feed_cache, article_store


# ── build_cache_key（不变） ────────────────────────────────

class TestBuildCacheKey:
    def test_basic(self):
        key = BackgroundRefresher.build_cache_key("afdian", ["author1"])
        assert key == "afdian/author1"

    def test_multiple_path_params(self):
        key = BackgroundRefresher.build_cache_key("afdian", ["a", "b"])
        assert key == "afdian/a/b"


# ── trigger（泛化） ───────────────────────────────────────

class TestTrigger:
    @pytest.mark.asyncio
    async def test_trigger_creates_task(self, caches, multi_route_config):
        feed_cache, article_store = caches
        refresher = BackgroundRefresher(feed_cache, article_store, multi_route_config)

        with patch.object(
            refresher, "_refresh_one", new_callable=AsyncMock
        ) as mock_refresh:
            await refresher.trigger("afdian", ["author1"], {"limit": "10"})
            await asyncio.sleep(0.1)
            mock_refresh.assert_called_once_with(
                "afdian", ["author1"], fetch_kwargs={"limit": "10"}
            )

    @pytest.mark.asyncio
    async def test_trigger_dedup(self, caches, multi_route_config):
        feed_cache, article_store = caches
        refresher = BackgroundRefresher(feed_cache, article_store, multi_route_config)

        cache_key = BackgroundRefresher.build_cache_key("afdian", ["author1"])
        refresher._pending.add(cache_key)

        with patch.object(
            refresher, "_refresh_one", new_callable=AsyncMock
        ) as mock_refresh:
            await refresher.trigger("afdian", ["author1"], {"limit": "10"})
            await asyncio.sleep(0.1)
            mock_refresh.assert_not_called()

    @pytest.mark.asyncio
    async def test_trigger_injects_feed_config_params(self, caches, multi_route_config):
        """trigger 从 feeds 配置补齐 feed_conf 中的字段（如 limit）"""
        feed_cache, article_store = caches
        refresher = BackgroundRefresher(feed_cache, article_store, multi_route_config)

        with patch.object(
            refresher, "_refresh_one", new_callable=AsyncMock
        ) as mock_refresh:
            # 不传 limit，应该从 feed_conf 补齐
            await refresher.trigger("afdian", ["author1"], {})
            await asyncio.sleep(0.1)
            mock_refresh.assert_called_once_with(
                "afdian", ["author1"], fetch_kwargs={"user_id": "author1", "limit": 5}
            )


# ── _refresh_one（不变） ───────────────────────────────────

class TestRefreshOne:
    @pytest.mark.asyncio
    async def test_success_updates_status(self, caches, multi_route_config):
        feed_cache, article_store = caches
        refresher = BackgroundRefresher(feed_cache, article_store, multi_route_config)

        mock_route = MagicMock()
        mock_route.feed_info = AsyncMock(
            return_value=MagicMock(title="t", link="l", description="d")
        )
        mock_route.fetch = AsyncMock(return_value=[])

        mock_registry = {"afdian": MagicMock(return_value=mock_route)}

        with (
            patch("RSSGen.core.refresher.get_registry", return_value=mock_registry),
            patch("RSSGen.core.refresher.generate_feed", return_value="<feed/>"),
        ):
            await refresher._refresh_one(
                "afdian", ["author1"], fetch_kwargs={"limit": "5"}
            )

        cache_key = "afdian/author1"
        assert cache_key in refresher._error_status
        assert refresher._error_status[cache_key]["error"] is None
        assert cache_key not in refresher._pending

    @pytest.mark.asyncio
    async def test_failure_records_error(self, caches, multi_route_config):
        feed_cache, article_store = caches
        refresher = BackgroundRefresher(feed_cache, article_store, multi_route_config)

        mock_registry = {"afdian": MagicMock(side_effect=RuntimeError("boom"))}

        with patch("RSSGen.core.refresher.get_registry", return_value=mock_registry):
            await refresher._refresh_one(
                "afdian", ["author1"], fetch_kwargs={"limit": "5"}
            )

        cache_key = "afdian/author1"
        assert refresher._error_status[cache_key]["error"] is not None
        assert cache_key not in refresher._pending


# ── start / stop（per-route tasks） ────────────────────────

class TestStartStop:
    @pytest.mark.asyncio
    async def test_start_creates_per_route_tasks(self, caches, multi_route_config):
        feed_cache, article_store = caches
        refresher = BackgroundRefresher(feed_cache, article_store, multi_route_config)

        with patch.object(refresher, "_run_route_loop", new_callable=AsyncMock):
            await refresher.start()
            assert "afdian" in refresher._tasks
            assert "zhihu" in refresher._tasks
            assert len(refresher._tasks) == 2
            await refresher.stop()

    @pytest.mark.asyncio
    async def test_stop_cancels_all_tasks(self, caches, multi_route_config):
        feed_cache, article_store = caches
        refresher = BackgroundRefresher(feed_cache, article_store, multi_route_config)

        with patch.object(refresher, "_run_route_loop", new_callable=AsyncMock):
            await refresher.start()
            await refresher.stop()
            assert refresher._tasks == {}

    @pytest.mark.asyncio
    async def test_idempotent_start(self, caches, multi_route_config):
        """重复调用 start 不会创建重复 task"""
        feed_cache, article_store = caches
        refresher = BackgroundRefresher(feed_cache, article_store, multi_route_config)

        with patch.object(refresher, "_run_route_loop", new_callable=AsyncMock):
            await refresher.start()
            tasks_after_first = dict(refresher._tasks)
            await refresher.start()
            assert refresher._tasks == tasks_after_first
            await refresher.stop()


# ── 预热判定 ──────────────────────────────────────────────

class TestPreheatDecision:
    @pytest.mark.asyncio
    async def test_preheat_skipped_when_disabled(self, caches, multi_route_config):
        """preheat_on_startup=false 时不调用 _refresh_one"""
        multi_route_config["routes"]["afdian"]["preheat_on_startup"] = False
        feed_cache, article_store = caches
        refresher = BackgroundRefresher(feed_cache, article_store, multi_route_config)

        with patch.object(refresher, "_refresh_one", new_callable=AsyncMock) as mock:
            await refresher._run_route_loop("afdian")
            mock.assert_not_called()

    @pytest.mark.asyncio
    async def test_preheat_skipped_when_already_has_data(self, caches, multi_route_config):
        """preheat_on_startup=true 但已有数据时跳过预热"""
        multi_route_config["routes"]["afdian"]["preheat_on_startup"] = True
        feed_cache, article_store = caches
        article_store.has_articles = AsyncMock(return_value=True)
        refresher = BackgroundRefresher(feed_cache, article_store, multi_route_config)

        with patch.object(refresher, "_refresh_one", new_callable=AsyncMock) as mock:
            await refresher._run_route_loop("afdian")
            mock.assert_not_called()

    @pytest.mark.asyncio
    async def test_preheat_runs_when_enabled_and_no_data(self, caches, multi_route_config):
        """preheat_on_startup=true 且无数据时执行预热"""
        multi_route_config["routes"]["afdian"]["preheat_on_startup"] = True
        multi_route_config["routes"]["afdian"]["refresh_interval"] = 0  # 不跑定时刷新
        feed_cache, article_store = caches
        article_store.has_articles = AsyncMock(return_value=False)
        refresher = BackgroundRefresher(feed_cache, article_store, multi_route_config)

        with patch.object(refresher, "_refresh_one", new_callable=AsyncMock) as mock:
            await refresher._run_route_loop("afdian")
            mock.assert_called_once_with(
                "afdian", ["author1"], fetch_kwargs={"user_id": "author1", "limit": 5}
            )


# ── refresh_interval 控制 ─────────────────────────────────

class TestRefreshInterval:
    @pytest.mark.asyncio
    async def test_zero_interval_exits_after_preheat(self, caches, multi_route_config):
        """refresh_interval=0 时，路由 task 在预热后退出"""
        multi_route_config["routes"]["afdian"]["refresh_interval"] = 0
        multi_route_config["routes"]["afdian"]["preheat_on_startup"] = False
        feed_cache, article_store = caches
        refresher = BackgroundRefresher(feed_cache, article_store, multi_route_config)

        # 如果 refresh_interval=0，_run_route_loop 应该直接返回
        await refresher._run_route_loop("afdian")
        # 不会卡在 while True 循环里 — 测试不会超时即说明正确

    @pytest.mark.asyncio
    async def test_null_interval_exits_after_preheat(self, caches, multi_route_config):
        """refresh_interval=None 时，路由 task 在预热后退出"""
        multi_route_config["routes"]["afdian"]["refresh_interval"] = None
        multi_route_config["routes"]["afdian"]["preheat_on_startup"] = False
        feed_cache, article_store = caches
        refresher = BackgroundRefresher(feed_cache, article_store, multi_route_config)

        await refresher._run_route_loop("afdian")


# ── preinit ───────────────────────────────────────────────

class TestPreinit:
    @pytest.mark.asyncio
    async def test_preinit_skipped_when_url_is_none(self, caches, multi_route_config):
        """preinit_url=None 时跳过预初始化"""
        multi_route_config["refresher"]["preinit_url"] = None
        feed_cache, article_store = caches
        refresher = BackgroundRefresher(feed_cache, article_store, multi_route_config)

        with patch("RSSGen.core.refresher.AsyncSession") as mock_session:
            await refresher._preinit_curl_cffi()
            mock_session.assert_not_called()

    @pytest.mark.asyncio
    async def test_preinit_runs_once_at_start(self, caches, multi_route_config):
        """start() 时 preinit 只调用一次（进程级幂等）"""
        multi_route_config["refresher"]["preinit_url"] = "https://cn.bing.com/"
        feed_cache, article_store = caches
        refresher = BackgroundRefresher(feed_cache, article_store, multi_route_config)

        with (
            patch.object(refresher, "_preinit_curl_cffi", new_callable=AsyncMock) as mock_preinit,
            patch.object(refresher, "_run_route_loop", new_callable=AsyncMock),
        ):
            await refresher.start()
            mock_preinit.assert_called_once()
            await refresher.stop()


# ── feed_id_field 泛化 ────────────────────────────────────

class TestFeedIdField:
    @pytest.mark.asyncio
    async def test_custom_feed_id_field(self, caches, multi_route_config):
        """自定义 feed_id_field 时，refresher 从 feed_conf 正确取值"""
        multi_route_config["routes"]["afdian"]["feeds"] = [
            {"custom_key": "author1", "limit": 5}
        ]
        feed_cache, article_store = caches
        refresher = BackgroundRefresher(feed_cache, article_store, multi_route_config)

        # mock 一个自定义 feed_id_field 的路由类
        mock_route_cls = MagicMock()
        mock_route_cls.feed_id_field = "custom_key"

        with patch("RSSGen.core.refresher.get_registry", return_value={"afdian": mock_route_cls}):
            feed_conf = refresher._find_feed_config("afdian", "author1")
            assert feed_conf is not None
            assert feed_conf["custom_key"] == "author1"


# ── get_status 按路由分组 ─────────────────────────────────

class TestGetStatus:
    def test_status_returns_route_grouped_dict(self, caches, multi_route_config):
        feed_cache, article_store = caches
        refresher = BackgroundRefresher(feed_cache, article_store, multi_route_config)

        # 模拟一些状态数据
        refresher._error_status = {
            "afdian/author1": {"last_success": "2026-01-01T00:00:00", "error": None, "item_count": 5},
            "zhihu/user1": {"last_success": "2026-01-01T00:00:00", "error": None, "item_count": 3},
        }

        status = refresher.get_status()
        assert "afdian" in status
        assert "zhihu" in status
        assert "author1" in status["afdian"]
        assert "user1" in status["zhihu"]


# ── _refresh_feeds fetch_kwargs 透传 ──────────────────────

class TestFetchKwargsPassthrough:
    @pytest.mark.asyncio
    async def test_extra_fields_passed_to_fetch(self, caches, multi_route_config):
        """feed_conf 中除 user_id/alias 外的字段透传给 fetch"""
        multi_route_config["routes"]["afdian"]["feeds"] = [
            {"user_id": "author1", "alias": "作者A", "limit": 15, "custom_param": "value"}
        ]
        feed_cache, article_store = caches
        refresher = BackgroundRefresher(feed_cache, article_store, multi_route_config)

        with patch.object(refresher, "_refresh_one", new_callable=AsyncMock) as mock:
            await refresher._refresh_feeds("测试", "afdian")
            call_kwargs = mock.call_args[0][2]  # fetch_kwargs
            assert call_kwargs["limit"] == 15
            assert call_kwargs["custom_param"] == "value"
            assert "user_id" in call_kwargs  # feed_id 也透传
            assert "alias" not in call_kwargs  # alias 不透传
```

- [ ] **Step 2: 运行测试确认失败**

```bash
cd /mnt/d/MyProject/Python/RSSGen && uv run pytest tests/test_refresher.py -v
```

预期：大量 FAIL — `BackgroundRefresher` 接口不匹配

- [ ] **Step 3: 重写 refresher.py**

完整替换 `RSSGen/core/refresher.py`：

```python
"""后台调度器：路由无关的预热与定期刷新"""

import asyncio
from datetime import datetime, timezone

from curl_cffi.const import CurlOpt
from curl_cffi.requests import AsyncSession
from loguru import logger

from RSSGen.core.cache import Cache
from RSSGen.core.feed import generate_feed
from RSSGen.routes import get_registry

DEFAULT_STARTUP_DELAY = 5
DEFAULT_MAX_RETRIES = 3
DEFAULT_RETRY_BASE_DELAY = 5


class BackgroundRefresher:
    def __init__(self, feed_cache: Cache, article_store, config: dict):
        self.feed_cache = feed_cache
        self.article_store = article_store
        self.config = config
        self._tasks: dict[str, asyncio.Task] = {}
        self._pending: set[str] = set()
        self._error_status: dict[str, dict] = {}

        refresher_config = config.get("refresher", {})
        self.startup_delay = refresher_config.get(
            "startup_delay", DEFAULT_STARTUP_DELAY
        )
        self.max_retries = refresher_config.get("max_retries", DEFAULT_MAX_RETRIES)
        self.retry_base_delay = refresher_config.get(
            "retry_base_delay", DEFAULT_RETRY_BASE_DELAY
        )
        self.preinit_url = refresher_config.get("preinit_url", "https://cn.bing.com/")

    async def start(self):
        if self._tasks:
            return

        await self._preinit_curl_cffi()

        routes_config = self.config.get("routes", {})
        for route_name, route_conf in routes_config.items():
            if not route_conf.get("enabled", False):
                continue
            if not route_conf.get("feeds"):
                logger.info(f"路由 {route_name} 未配置 feeds，跳过调度")
                continue
            self._tasks[route_name] = asyncio.create_task(
                self._run_route_loop(route_name)
            )
            logger.info(f"路由 {route_name} 调度任务已创建")

        logger.info(f"BackgroundRefresher 已启动，共 {len(self._tasks)} 个路由任务")

    async def stop(self):
        for name, task in self._tasks.items():
            task.cancel()
            try:
                await task
            except asyncio.CancelledError:
                pass
        self._tasks.clear()
        logger.info("BackgroundRefresher 已停止")

    async def trigger(
        self, route_name: str, path_params: list[str], query_params: dict | None = None
    ):
        """动态触发：未知feed首次访问时调用，非阻塞"""
        if self.build_cache_key(route_name, path_params) in self._pending:
            return

        fetch_kwargs = dict(query_params or {})
        if path_params:
            feed_conf = self._find_feed_config(route_name, path_params[0])
            if feed_conf:
                route_cls = get_registry().get(route_name)
                feed_id_field = getattr(route_cls, "feed_id_field", "user_id") if route_cls else "user_id"
                for key, value in feed_conf.items():
                    if key != feed_id_field and key != "alias" and key not in fetch_kwargs:
                        fetch_kwargs[key] = value

        asyncio.create_task(
            self._refresh_one(route_name, path_params, fetch_kwargs=fetch_kwargs)
        )

    def _find_feed_config(self, route_name: str, feed_id: str) -> dict | None:
        feeds = self.config.get("routes", {}).get(route_name, {}).get("feeds", [])
        route_cls = get_registry().get(route_name)
        feed_id_field = getattr(route_cls, "feed_id_field", "user_id") if route_cls else "user_id"
        for fc in feeds:
            if fc.get(feed_id_field) == feed_id:
                return fc
        return None

    def get_status(self) -> dict:
        """返回按路由分组的状态"""
        grouped: dict[str, dict] = {}
        for cache_key, status in self._error_status.items():
            parts = cache_key.split("/", 1)
            if len(parts) == 2:
                route_name, feed_id = parts
            else:
                route_name = cache_key
                feed_id = ""
            grouped.setdefault(route_name, {})[feed_id] = status
        return grouped

    @staticmethod
    def build_cache_key(route_name: str, path_params: list[str]) -> str:
        return f"{route_name}/{'/'.join(path_params)}"

    async def _preinit_curl_cffi(self):
        """对目标站点发起一次无害请求，强制底层 libcurl 正确加载"""
        if not self.preinit_url:
            logger.info("preinit_url 为空，跳过 HTTP 客户端预初始化")
            return
        try:
            async with AsyncSession(
                impersonate="chrome131",
                curl_options={CurlOpt.FRESH_CONNECT: True},
            ) as session:
                resp = await session.get(self.preinit_url, timeout=10)
                if resp.status_code == 200:
                    logger.info("HTTP 客户端预初始化成功")
                else:
                    logger.warning(f"HTTP 客户端预初始化响应异常: {resp.status_code}")
        except Exception as e:
            logger.warning(f"HTTP 客户端预初始化失败: {e}，将继续尝试正常请求")

    async def _run_route_loop(self, route_name: str):
        """单路由生命周期：startup_delay → 预热判定 → 周期刷新循环"""
        try:
            logger.info(f"[{route_name}] 等待 {self.startup_delay} 秒确保网络就绪...")
            await asyncio.sleep(self.startup_delay)

            # ── 预热阶段 ──
            route_conf = self.config.get("routes", {}).get(route_name, {})
            preheat = route_conf.get("preheat_on_startup", False)

            if preheat:
                has_data = await self.article_store.has_articles(route_name)
                if has_data:
                    logger.info(f"[{route_name}] 路由已有数据，跳过预热")
                else:
                    logger.info(f"[{route_name}] 开始预热")
                    await self._refresh_feeds("预热", route_name)
            else:
                logger.info(f"[{route_name}] 预热已关闭（preheat_on_startup=false）")

            # ── 周期刷新阶段 ──
            refresh_interval = route_conf.get("refresh_interval")
            if not refresh_interval:
                logger.info(f"[{route_name}] 未配置 refresh_interval，定时刷新已关闭")
                return

            while True:
                await asyncio.sleep(refresh_interval)
                try:
                    await self._refresh_feeds("定时刷新", route_name)
                except Exception:
                    logger.exception(f"[{route_name}] 定时刷新异常，将在下一轮重试")
        except asyncio.CancelledError:
            raise
        except Exception:
            logger.exception(f"[{route_name}] 路由调度任务异常退出")

    async def _refresh_feeds(self, label: str, route_name: str):
        feeds = self.config.get("routes", {}).get(route_name, {}).get("feeds", [])
        if not feeds:
            logger.info(f"[{route_name}] 未配置 feed 列表，跳过{label}")
            return

        route_cls = get_registry().get(route_name)
        feed_id_field = getattr(route_cls, "feed_id_field", "user_id") if route_cls else "user_id"

        logger.info(f"[{route_name}] 开始{label} {len(feeds)} 个 feed")
        for feed_conf in feeds:
            feed_id = feed_conf.get(feed_id_field)
            if not feed_id:
                logger.warning(f"[{route_name}] feed 配置缺少 {feed_id_field} 字段，跳过: {feed_conf}")
                continue
            # 构造 fetch_kwargs：透传 feed_conf 中除 feed_id_field 和 alias 之外的字段
            fetch_kwargs = {}
            for key, value in feed_conf.items():
                if key != feed_id_field and key != "alias":
                    fetch_kwargs[key] = value
            await self._refresh_one(route_name, [feed_id], fetch_kwargs=fetch_kwargs)
        logger.info(f"[{route_name}] {label}完成")

    async def _refresh_one(
        self, route_name: str, path_params: list[str], fetch_kwargs: dict | None = None
    ):
        cache_key = self.build_cache_key(route_name, path_params)

        if cache_key in self._pending:
            return
        self._pending.add(cache_key)

        route_cls = get_registry().get(route_name)
        if not route_cls:
            self._pending.discard(cache_key)
            raise ValueError(f"路由不存在: {route_name}")

        merged_config = {
            **self.config.get("scraper", {}),
            **self.config.get("routes", {}).get(route_name, {}),
        }
        kwargs = {**(fetch_kwargs or {}), "path_params": path_params}
        last_error: Exception | None = None

        try:
            for attempt in range(self.max_retries):
                if attempt > 0:
                    delay = self.retry_base_delay * (2 ** (attempt - 1))
                    logger.info(
                        f"重试 {cache_key} (第{attempt + 1}次)，等待 {delay} 秒..."
                    )
                    await asyncio.sleep(delay)
                else:
                    logger.info(f"正在刷新 {cache_key}")

                try:
                    route = route_cls(merged_config)
                    info = await route.feed_info(**kwargs)
                    items = await route.fetch(
                        article_store=self.article_store, **kwargs
                    )
                    xml = generate_feed(info, items, format="atom")
                    await self.feed_cache.set(cache_key, xml)

                    self._error_status[cache_key] = {
                        "last_success": datetime.now(timezone.utc).isoformat(),
                        "error": None,
                        "item_count": len(items),
                    }
                    logger.info(f"刷新完成 {cache_key}: {len(items)} 条目")
                    return
                except Exception as e:
                    last_error = e
                    logger.opt(exception=True).warning(
                        f"刷新失败 {cache_key} (第{attempt + 1}次): {e}"
                    )

            logger.error(f"刷新失败 {cache_key}: 所有 {self.max_retries} 次重试均失败")
            self._error_status[cache_key] = {
                "last_success": self._error_status.get(cache_key, {}).get(
                    "last_success"
                ),
                "error": str(last_error),
                "item_count": 0,
            }
        finally:
            self._pending.discard(cache_key)
```

- [ ] **Step 4: 运行测试确认通过**

```bash
cd /mnt/d/MyProject/Python/RSSGen && uv run pytest tests/test_refresher.py -v
```

预期：全部 PASS

- [ ] **Step 5: Commit**

```bash
git add RSSGen/core/refresher.py tests/test_refresher.py
git commit -m "refactor(refresher): 路由无关的通用调度器"
```

---

### Task 4: app.py 启动逻辑调整

**Files:**
- Modify: `RSSGen/app.py:33-51`

- [ ] **Step 1: 调整缓存 TTL 来源和 refresher 启动条件**

将 `startup()` 函数改为：

```python
@app.on_event("startup")
async def startup():
    global config, feed_cache, article_cache, article_store, refresher
    config = load_config()
    discover_routes()
    logger.info(f"已加载路由: {list(get_registry().keys())}")

    cache_config = config.get("cache", {})
    feed_cache = Cache(ttl=cache_config.get("feed_ttl", 21600))
    article_cache = Cache(ttl=cache_config.get("article_ttl", 43200))

    sqlite_path = config.get("storage", {}).get("sqlite_path", "./data/rssgen.db")
    article_store = SqliteArticleStore(sqlite_path)
    await article_store.init()

    routes_config = config.get("routes", {})
    if any(r.get("enabled", False) for r in routes_config.values()):
        refresher = BackgroundRefresher(feed_cache, article_store, config)
        await refresher.start()
```

- [ ] **Step 2: 确认 feed 路由处理逻辑无变化**

`/feed/{route_name}/{path:path}` 端点的逻辑不受影响，只读 `route_config` 做 merge，不依赖 afdian 特殊路径。

- [ ] **Step 3: 运行全量测试**

```bash
cd /mnt/d/MyProject/Python/RSSGen && uv run pytest -v
```

预期：全部 PASS

- [ ] **Step 4: Commit**

```bash
git add RSSGen/app.py
git commit -m "refactor(app): 缓存 TTL 改读全局 cache 段，refresher 按 enabled 路由聚合"
```

---

### Task 5: 配置文件同步

**Files:**
- Modify: `config.example.yml`
- Modify: `config.yml`

- [ ] **Step 1: 更新 config.example.yml**

```yaml
# RSSGen 配置文件
# 复制为 config.yml 并填入你自己的凭证

server:
  host: "0.0.0.0"
  port: 8000
  cache_ttl: 1800  # 默认缓存时间（秒）

# 持久化存储（文章正文持久化到 SQLite，避免重启重抓）
storage:
  sqlite_path: "./data/rssgen.db"   # 文章数据库路径，相对项目根；Docker 内会挂载

# 全局缓存 TTL
cache:
  feed_ttl: 21600       # Feed 级缓存 TTL，默认 6 小时（秒）
  article_ttl: 43200    # 文章级缓存 TTL，默认 12 小时（秒）

# 全局反爬配置
scraper:
  proxy: null  # HTTP/SOCKS5 代理，如 "socks5://127.0.0.1:1080"
  rate_limit: 1.0  # 全局最小请求间隔（秒）
  impersonate: "chrome131"  # curl_cffi 浏览器指纹模拟，可选值见 https://github.com/lexiforest/curl_cffi#supported-impersonate

# 后台刷新器配置（用于冷启动优化和定时刷新）
refresher:
  startup_delay: 5      # 启动延迟（秒），等待容器网络稳定
  max_retries: 3        # 最大重试次数
  retry_base_delay: 5   # 重试基础延迟（秒），指数退避
  preinit_url: "https://cn.bing.com/"  # HTTP 客户端预初始化 URL，null/空字符串则跳过

# ============ 路由配置 ============

routes:
  afdian:
    enabled: true
    cookie: "你的爱发电 Cookie（推荐使用 Cookie Master 扩展的 Flat Copy 功能）"
    # rate_limit: 0.5       # 可选：该路由的请求间隔
    # proxy: null            # 可选：该路由的代理
    # impersonate: chrome131 # 可选：该路由的浏览器指纹
    preheat_on_startup: false   # 预热开关，默认关闭；首次部署后手动打开一次即可
    refresh_interval: 14400     # 后台刷新间隔，默认 4 小时（秒）；缺省/0/null 则关闭定时刷新
    feeds:                      # 预热列表（可选，不配置则无预热）
      - user_id: "author1"     # 统一字段名（原 slug 已迁移为 user_id）
        alias: "作者别名"       # 可选，用于易读的 feed title
        limit: 20              # 获取最新 20 篇
      - user_id: "author2"
        limit: 0               # 0 = 获取全部文章

  zhihu:
    enabled: true
    cookie: "你的知乎 Cookie（必须包含 d_c0）"
    # rate_limit: 0.5          # 可选：该路由的请求间隔
    # proxy: null               # 可选：该路由的代理
    # impersonate: chrome131    # 可选：该路由的浏览器指纹
    preheat_on_startup: false   # 预热开关，默认关闭
    refresh_interval: 14400     # 后台刷新间隔，默认 4 小时（秒）
    feeds:                      # 预热列表（可选，不配置则无预热）
      - user_id: "your_user_id"  # 用户主页 URL token
        alias: "用户别名"        # 别名，用于易读的 feed title
        limit: 20               # 获取最新 20 篇
```

- [ ] **Step 2: 更新 config.yml**

同步 config.yml 的结构变更：
1. 新增 `cache:` 段（`feed_ttl` 和 `article_ttl` 从路由级移除）
2. 新增 `refresher.preinit_url`
3. 路由级新增 `preheat_on_startup: false`
4. afdian feeds 的 `slug:` 改为 `user_id:`
5. 删除路由级 `article_ttl` 和 `feed_ttl`

- [ ] **Step 3: Commit**

```bash
git add config.example.yml config.yml
git commit -m "config: 配置结构迁移 — 全局 cache 段、preheat 开关、slug→user_id"
```

---

### Task 6: afdian 路由代码同步（slug → user_id）

**Files:**
- Modify: `RSSGen/routes/afdian.py:147`

- [ ] **Step 1: 更新 lookup_alias 的 key 参数**

`afdian.py:147` 当前写死 `"slug"` 作为 `lookup_alias` 的 key：

```python
display_name = lookup_alias(self.config.get("feeds"), "slug", author_slug) or author_slug
```

config 迁移后 `slug:` 字段已改为 `user_id:`，所以这里也要改：

```python
display_name = lookup_alias(self.config.get("feeds"), "user_id", author_slug) or author_slug
```

注意：`author_slug` 变量名不变（它来自 URL path_params，仍然是 afdian 的 url_slug），只是从配置中查找时用 `user_id` 字段匹配。

- [ ] **Step 2: 确认 zhihu 路由无需改动**

zhihu 路由（`zhihu.py:64`）已经用 `"user_id"` 作为 key，无需修改。

```bash
grep -n "lookup_alias" RSSGen/routes/zhihu.py
```

预期：`lookup_alias(self.config.get("feeds"), "user_id", user_id)`

- [ ] **Step 3: 运行 afdian 相关测试**

```bash
cd /mnt/d/MyProject/Python/RSSGen && uv run pytest tests/test_afdian_pipeline.py tests/test_afdian_caching.py -v
```

预期：全部 PASS（注意这些测试可能需要同步更新 config 中的 `slug` → `user_id`，见 Task 7）

- [ ] **Step 4: Commit**

```bash
git add RSSGen/routes/afdian.py
git commit -m "fix(afdian): lookup_alias key 从 slug 改为 user_id"
```

---

### Task 7: 现有测试迁移

**Files:**
- Check: `tests/test_afdian_caching.py`
- Check: `tests/test_afdian_pipeline.py`
- Check: `tests/test_zhihu_route.py`

- [ ] **Step 1: 扫描引用旧字段的测试**

```bash
grep -rn "slug" tests/ --include="*.py"
grep -rn "routes.*afdian.*refresh_interval" tests/ --include="*.py"
grep -rn "feed_ttl\|article_ttl" tests/ --include="*.py"
```

- [ ] **Step 2: 按需更新**

对发现的旧字段引用，迁移到新结构（`slug` → `user_id`，`routes.afdian.refresh_interval` → 路由级 `refresh_interval`，`feed_ttl`/`article_ttl` → `cache` 段）。

- [ ] **Step 3: 运行全量测试确认无回归**

```bash
cd /mnt/d/MyProject/Python/RSSGen && uv run pytest -v
```

预期：全部 PASS

- [ ] **Step 4: Commit（如有改动）**

```bash
git add tests/
git commit -m "test: 迁移旧字段引用至新配置结构"
```

---

### Task 8: Docker Compose 与手动验证

**Files:**
- Check: `docker-compose.yml`

- [ ] **Step 1: 检查 docker-compose.yml 是否引用了旧配置路径**

```bash
grep -n "feed_ttl\|article_ttl\|slug" docker-compose.yml
```

预期：无引用（docker-compose 只挂载卷，不读配置字段）

- [ ] **Step 2: 手动验证清单**

按 spec 中的手动验证清单逐项确认：

1. **关闭预热**：`preheat_on_startup: false` + `refresh_interval: null` → 启动后 trigger 现场触发能正常返回 feed
2. **开预热但已有数据**：article_store 中 afdian 路由有任意文章 → 启动日志显示"afdian 路由已有数据，跳过预热"
3. **多路由**：afdian 关、zhihu 开 → 启动后只有 zhihu 跑预热和定时刷新
4. **preinit_url 改成不可达地址** → 启动有 warning 但不阻塞预热

- [ ] **Step 3: Final commit（如有遗漏）**

```bash
git add -A
git commit -m "chore: 配置同步与文档更新"
```
