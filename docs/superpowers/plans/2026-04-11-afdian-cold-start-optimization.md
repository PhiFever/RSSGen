# 爱发电路由冷启动延时优化 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 实现爱发电路由的冷启动延时优化，通过双层缓存和后台刷新机制，将HTTP请求响应时间从12+秒降低到<1ms（缓存命中时）

**Architecture:** 采用读写分离架构，HTTP请求只读缓存，后台调度器负责定期刷新feed；新增文章级缓存减少API调用；使用异步任务和内存缓存实现零冷启动

**Tech Stack:** FastAPI, asyncio, cachetools.TTLCache, pytest

**Design Spec:** `docs/superpowers/specs/2026-04-11-afdian-cold-start-optimization-design.md`

---

## File Structure

**Create:**
- `RSSGen/core/refresher.py` - BackgroundRefresher后台调度器，负责预热和定时刷新

**Modify:**
- `config.example.yml` - 新增refresh_interval、article_ttl、feed_ttl、feeds配置项
- `RSSGen/core/route.py` - `Route.fetch()`签名新增可选`article_cache`参数
- `RSSGen/routes/afdian.py` - 适配文章级缓存逻辑，跳过已缓存文章的API调用
- `RSSGen/app.py` - 启动预热、注册调度器、改造请求处理、新增`/status`端点

**Test:**
- `tests/test_refresher.py` - BackgroundRefresher核心行为测试
- `tests/test_afdian_caching.py` - 爱发电路由的缓存命中/未命中逻辑测试

## Task Breakdown

### Task 1: 更新配置文件模板

**Files:**
- Modify: `config.example.yml`

- [ ] **Step 1: 添加配置项到config.example.yml**

在 `routes.afdian` 部分，在已有的 `rate_limit` 和 `proxy` 注释之后添加：

```yaml
    refresh_interval: 14400     # 后台刷新间隔，默认 4 小时（秒）
    article_ttl: 43200          # 文章级缓存 TTL，默认 12 小时（秒）
    feed_ttl: 21600             # Feed 级缓存 TTL，默认 6 小时（秒）
    feeds:                      # 预热列表（可选，不配置则无预热）
      - slug: "author1"
        limit: 20
      - slug: "author2"
        limit: 10
```

- [ ] **Step 2: 验证配置格式正确**

Run: `python -c "import yaml; data = yaml.safe_load(open('config.example.yml')); print(data['routes']['afdian']['feeds'])"`
Expected: 输出 feeds 列表

- [ ] **Step 3: 提交配置变更**

### Task 2: 扩展Route基类支持文章级缓存

**Files:**
- Modify: `RSSGen/core/route.py:37`

- [ ] **Step 1: 修改Route.fetch()方法签名**

当前签名：
```python
    async def fetch(self, **kwargs) -> list[FeedItem]:
        raise NotImplementedError
```

改为：
```python
    async def fetch(self, article_cache=None, **kwargs) -> list[FeedItem]:
        raise NotImplementedError
```

`article_cache` 作为显式可选参数，让子类可以通过命名参数接收，也可以忽略。

- [ ] **Step 2: 验证语法**

Run: `python -m py_compile RSSGen/core/route.py`
Expected: 无输出

- [ ] **Step 3: 提交变更**

### Task 3: 创建BackgroundRefresher后台调度器

**Files:**
- Create: `RSSGen/core/refresher.py`

注意：refresher 接收**全局 config**（顶层字典），而非路由级 config，因为 `_refresh_one` 需要根据 route_name 查找对应路由的配置。

- [ ] **Step 1: 创建完整的BackgroundRefresher类**

```python
"""后台调度器：负责预热和定期刷新feed缓存"""

import asyncio
import logging
from datetime import datetime, timezone

from RSSGen.core.cache import Cache

logger = logging.getLogger("rssgen.refresher")


class BackgroundRefresher:
    def __init__(self, feed_cache: Cache, article_cache: Cache, config: dict):
        """
        参数:
            feed_cache: Feed级缓存实例
            article_cache: 文章级缓存实例
            config: 全局配置字典（顶层，包含 routes 等）
        """
        self.feed_cache = feed_cache
        self.article_cache = article_cache
        self.config = config
        self._task: asyncio.Task | None = None
        self._pending: set[str] = set()
        self._error_status: dict[str, dict] = {}

    async def start(self):
        """启动预热 + 定时循环"""
        if self._task is None:
            self._task = asyncio.create_task(self._run_loop())
            logger.info("BackgroundRefresher 已启动")

    async def stop(self):
        """优雅停止"""
        if self._task:
            self._task.cancel()
            try:
                await self._task
            except asyncio.CancelledError:
                pass
            self._task = None
            logger.info("BackgroundRefresher 已停止")

    async def trigger(self, route_name: str, path_params: list[str], query_params: dict):
        """动态触发：未知feed首次访问时调用，非阻塞"""
        cache_key = self._build_cache_key(route_name, path_params, query_params)
        if cache_key not in self._pending:
            asyncio.create_task(self._refresh_one(route_name, path_params, query_params))

    def get_status(self) -> dict:
        """返回所有feed的刷新状态"""
        return self._error_status.copy()

    # ---- 内部方法 ----

    @staticmethod
    def _build_cache_key(route_name: str, path_params: list[str], query_params: dict) -> str:
        path = "/".join(path_params)
        query = "&".join(f"{k}={v}" for k, v in sorted(query_params.items()))
        return f"{route_name}/{path}?{query}"

    async def _run_loop(self):
        """主循环：预热 + 定时刷新"""
        try:
            await self._warmup()

            # 从第一个启用了 feeds 的路由获取 refresh_interval
            # 目前只有 afdian，后续扩展时可改为按路由独立调度
            afdian_config = self.config.get("routes", {}).get("afdian", {})
            refresh_interval = afdian_config.get("refresh_interval", 14400)

            while True:
                await asyncio.sleep(refresh_interval)
                await self._refresh_all()
        except asyncio.CancelledError:
            raise
        except Exception:
            logger.exception("BackgroundRefresher 主循环异常退出")

    async def _warmup(self):
        """预热已配置的feed"""
        afdian_config = self.config.get("routes", {}).get("afdian", {})
        feeds = afdian_config.get("feeds", [])
        if not feeds:
            logger.info("未配置预热列表，跳过预热")
            return

        logger.info(f"开始预热 {len(feeds)} 个 feed")
        for feed_conf in feeds:
            slug = feed_conf.get("slug")
            limit = feed_conf.get("limit", 20)
            if slug:
                await self._refresh_one("afdian", [slug], {"limit": str(limit)})
        logger.info("预热完成")

    async def _refresh_all(self):
        """刷新所有已配置的feed"""
        afdian_config = self.config.get("routes", {}).get("afdian", {})
        feeds = afdian_config.get("feeds", [])
        if not feeds:
            return

        logger.info(f"开始定时刷新 {len(feeds)} 个 feed")
        for feed_conf in feeds:
            slug = feed_conf.get("slug")
            limit = feed_conf.get("limit", 20)
            if slug:
                await self._refresh_one("afdian", [slug], {"limit": str(limit)})
        logger.info("定时刷新完成")

    async def _refresh_one(self, route_name: str, path_params: list[str], query_params: dict):
        """刷新单个feed"""
        from RSSGen.core.feed import generate_feed
        from RSSGen.routes import get_registry

        cache_key = self._build_cache_key(route_name, path_params, query_params)

        if cache_key in self._pending:
            return

        self._pending.add(cache_key)
        try:
            logger.info(f"正在刷新 {cache_key}")

            registry = get_registry()
            route_cls = registry.get(route_name)
            if not route_cls:
                raise ValueError(f"路由不存在: {route_name}")

            # 使用对应路由的配置实例化
            route_config = self.config.get("routes", {}).get(route_name, {})
            route = route_cls(route_config)

            kwargs = dict(query_params)
            kwargs["path_params"] = path_params

            info = await route.feed_info(**kwargs)
            items = await route.fetch(article_cache=self.article_cache, **kwargs)

            xml = generate_feed(info, items, format="atom")
            await self.feed_cache.set(cache_key, xml)

            self._error_status[cache_key] = {
                "last_success": datetime.now(timezone.utc).isoformat(),
                "error": None,
                "item_count": len(items),
            }
            logger.info(f"刷新完成 {cache_key}: {len(items)} 条目")

        except Exception as e:
            logger.error(f"刷新失败 {cache_key}: {e}")
            self._error_status[cache_key] = {
                "last_success": self._error_status.get(cache_key, {}).get("last_success"),
                "error": str(e),
                "item_count": 0,
            }
        finally:
            self._pending.discard(cache_key)
```

- [ ] **Step 2: 验证语法和导入**

Run: `python -m py_compile RSSGen/core/refresher.py`
Expected: 无输出

- [ ] **Step 3: 提交**

### Task 4: 修改爱发电路由支持文章级缓存

**Files:**
- Modify: `RSSGen/routes/afdian.py:100-140`

- [ ] **Step 1: 修改fetch方法**

替换整个 `fetch` 方法（第100-140行）。关键改动：
1. 新增 `article_cache` 参数
2. 遍历文章时先查 ArticleCache
3. 使用 `cache_hit` 标志控制 rate_limit sleep（而非复用 `content` 变量）

```python
    async def fetch(self, article_cache=None, **kwargs) -> list[FeedItem]:
        path_params: list[str] = kwargs.get("path_params", [])
        if not path_params:
            raise ValueError("需要指定作者 url_slug，如 /feed/afdian/{author_slug}")
        author_slug = path_params[0]

        limit = int(kwargs.get("limit", 20))

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
            article_cache_key = f"article:afdian:{post_id}"

            # 优先读文章缓存
            cache_hit = False
            content = None
            if article_cache:
                content = await article_cache.get(article_cache_key)
                if content is not None:
                    cache_hit = True

            # 缓存未命中才调 API
            if not cache_hit:
                content = await self._get_post_detail(scraper, post_id)
                if article_cache and content:
                    await article_cache.set(article_cache_key, content)
                await asyncio.sleep(self.config.get("rate_limit", 0.5))

            items.append(FeedItem(
                title=post.get("title", "无标题"),
                link=f"{HOST_URL}/p/{post_id}",
                content=content or "",
                pub_date=pub_date,
                author=post.get("user", {}).get("name"),
                guid=post_id,
                enclosures=enclosures,
            ))

        return items
```

- [ ] **Step 2: 验证语法**

Run: `python -m py_compile RSSGen/routes/afdian.py`
Expected: 无输出

- [ ] **Step 3: 提交变更**

### Task 5: 修改app.py支持读写分离架构

**Files:**
- Modify: `RSSGen/app.py`

改动点：初始化双层缓存和refresher、改造startup/shutdown、改造feed端点、新增/status端点。

注意：空feed直接调用现有的 `generate_feed(info, [], format=fmt)` 即可，**不需要** 新建 `generate_empty_feed` 函数。

- [ ] **Step 1: 修改模块级变量和导入**

在现有导入之后新增：
```python
from RSSGen.core.refresher import BackgroundRefresher
```

替换现有的 `cache` 变量：
```python
# 原来：cache = Cache()
# 改为：
feed_cache = Cache()     # TTL 在 startup 中根据配置重新初始化
article_cache = Cache()  # TTL 在 startup 中根据配置重新初始化
refresher: BackgroundRefresher | None = None
```

- [ ] **Step 2: 修改startup函数**

```python
@app.on_event("startup")
async def startup():
    global config, feed_cache, article_cache, refresher
    config = load_config()
    discover_routes()
    route_names = list(get_registry().keys())
    logger.info(f"已加载路由: {route_names}")

    # 根据配置初始化双层缓存
    afdian_config = config.get("routes", {}).get("afdian", {})
    feed_ttl = afdian_config.get("feed_ttl", 21600)
    article_ttl = afdian_config.get("article_ttl", 43200)
    feed_cache = Cache(ttl=feed_ttl)
    article_cache = Cache(ttl=article_ttl)

    # 启动后台刷新器（传入全局 config）
    if afdian_config.get("enabled", False):
        refresher = BackgroundRefresher(feed_cache, article_cache, config)
        await refresher.start()
```

- [ ] **Step 3: 添加shutdown事件**

```python
@app.on_event("shutdown")
async def shutdown():
    global refresher
    if refresher:
        await refresher.stop()
```

- [ ] **Step 4: 修改feed端点**

```python
@app.get("/feed/{route_name}/{path:path}")
async def feed(route_name: str, path: str, request: Request):
    registry = get_registry()
    route_cls = registry.get(route_name)
    if not route_cls:
        raise HTTPException(status_code=404, detail=f"路由不存在: {route_name}")

    cache_key = f"{route_name}/{path}?{request.url.query}"

    # 1. 检查feed缓存（所有路由共用）
    cached = await feed_cache.get(cache_key)
    if cached:
        return Response(content=cached, media_type="application/xml; charset=utf-8")

    # 2. 解析参数
    path_parts = path.strip("/").split("/") if path.strip("/") else []
    kwargs = {}
    if path_parts:
        kwargs["path_params"] = path_parts
    kwargs.update(request.query_params)

    # 3. 如果有后台刷新器且该路由已启用，走异步模式
    if refresher:
        route_config = config.get("routes", {}).get(route_name, {})
        if route_config.get("enabled", False) and route_config.get("feeds") is not None:
            # 触发后台刷新（非阻塞）
            await refresher.trigger(route_name, path_parts, dict(request.query_params))

            # 返回合法的空feed
            route = route_cls(route_config)
            info = await route.feed_info(**kwargs)
            fmt = request.query_params.get("format", "atom")
            xml = generate_feed(info, [], format=fmt)
            return Response(content=xml, media_type="application/xml; charset=utf-8")

    # 4. 其他路由或未启用后台刷新，走原有同步逻辑
    route_config = config.get("routes", {}).get(route_name, {})
    route = route_cls(route_config)

    try:
        info = await route.feed_info(**kwargs)
        items = await route.fetch(**kwargs)
    except Exception as e:
        logger.exception(f"路由 {route_name} 抓取失败")
        raise HTTPException(status_code=502, detail=f"抓取失败: {e}")

    fmt = request.query_params.get("format", "atom")
    xml = generate_feed(info, items, format=fmt)

    await feed_cache.set(cache_key, xml)
    return Response(content=xml, media_type="application/xml; charset=utf-8")
```

- [ ] **Step 5: 添加/status端点**

```python
@app.get("/status")
async def status():
    if not refresher:
        return {"enabled": False, "message": "后台刷新未启用"}

    return {
        "enabled": True,
        "feeds": refresher.get_status(),
    }
```

- [ ] **Step 6: 验证语法**

Run: `python -m py_compile RSSGen/app.py`
Expected: 无输出

- [ ] **Step 7: 提交app.py改造**

### Task 6: 编写测试

**Files:**
- Create: `tests/test_refresher.py`
- Create: `tests/test_afdian_caching.py`

- [ ] **Step 1: 创建tests目录和conftest**

```bash
mkdir -p tests
touch tests/__init__.py
```

- [ ] **Step 2: 创建test_refresher.py**

测试 BackgroundRefresher 的核心行为，使用 mock 替代真实路由和网络请求。

```python
"""BackgroundRefresher 核心行为测试"""

import asyncio

import pytest
from unittest.mock import AsyncMock, MagicMock, patch

from RSSGen.core.cache import Cache
from RSSGen.core.refresher import BackgroundRefresher


@pytest.fixture
def global_config():
    return {
        "routes": {
            "afdian": {
                "enabled": True,
                "cookie": "test_cookie",
                "refresh_interval": 1,  # 短间隔便于测试
                "feeds": [
                    {"slug": "author1", "limit": 5},
                ],
            }
        }
    }


@pytest.fixture
def caches():
    feed_cache = Cache(ttl=60)
    article_cache = Cache(ttl=60)
    return feed_cache, article_cache


class TestBuildCacheKey:
    def test_basic(self):
        key = BackgroundRefresher._build_cache_key("afdian", ["author1"], {"limit": "10"})
        assert key == "afdian/author1?limit=10"

    def test_empty_query(self):
        key = BackgroundRefresher._build_cache_key("afdian", ["author1"], {})
        assert key == "afdian/author1?"

    def test_query_sorted(self):
        key = BackgroundRefresher._build_cache_key("afdian", ["a"], {"z": "1", "a": "2"})
        assert key == "afdian/a?a=2&z=1"


class TestTrigger:
    @pytest.mark.asyncio
    async def test_trigger_creates_task(self, caches, global_config):
        feed_cache, article_cache = caches
        refresher = BackgroundRefresher(feed_cache, article_cache, global_config)

        with patch.object(refresher, "_refresh_one", new_callable=AsyncMock) as mock_refresh:
            await refresher.trigger("afdian", ["author1"], {"limit": "10"})
            await asyncio.sleep(0.1)  # 让 create_task 有机会执行
            mock_refresh.assert_called_once_with("afdian", ["author1"], {"limit": "10"})

    @pytest.mark.asyncio
    async def test_trigger_dedup(self, caches, global_config):
        """已在刷新中的feed不会重复触发"""
        feed_cache, article_cache = caches
        refresher = BackgroundRefresher(feed_cache, article_cache, global_config)

        cache_key = BackgroundRefresher._build_cache_key("afdian", ["author1"], {"limit": "10"})
        refresher._pending.add(cache_key)

        with patch.object(refresher, "_refresh_one", new_callable=AsyncMock) as mock_refresh:
            await refresher.trigger("afdian", ["author1"], {"limit": "10"})
            await asyncio.sleep(0.1)
            mock_refresh.assert_not_called()


class TestRefreshOne:
    @pytest.mark.asyncio
    async def test_success_updates_status(self, caches, global_config):
        feed_cache, article_cache = caches
        refresher = BackgroundRefresher(feed_cache, article_cache, global_config)

        mock_route = MagicMock()
        mock_route.feed_info = AsyncMock(return_value=MagicMock(title="t", link="l", description="d"))
        mock_route.fetch = AsyncMock(return_value=[])

        mock_registry = {"afdian": MagicMock(return_value=mock_route)}

        with patch("RSSGen.core.refresher.get_registry", return_value=mock_registry), \
             patch("RSSGen.core.refresher.generate_feed", return_value="<feed/>"):
            await refresher._refresh_one("afdian", ["author1"], {"limit": "5"})

        cache_key = "afdian/author1?limit=5"
        assert cache_key in refresher._error_status
        assert refresher._error_status[cache_key]["error"] is None
        assert cache_key not in refresher._pending  # finally 中已移除

    @pytest.mark.asyncio
    async def test_failure_records_error(self, caches, global_config):
        feed_cache, article_cache = caches
        refresher = BackgroundRefresher(feed_cache, article_cache, global_config)

        mock_registry = {"afdian": MagicMock(side_effect=RuntimeError("boom"))}

        with patch("RSSGen.core.refresher.get_registry", return_value=mock_registry):
            await refresher._refresh_one("afdian", ["author1"], {"limit": "5"})

        cache_key = "afdian/author1?limit=5"
        assert refresher._error_status[cache_key]["error"] is not None
        assert cache_key not in refresher._pending


class TestStartStop:
    @pytest.mark.asyncio
    async def test_start_and_stop(self, caches, global_config):
        feed_cache, article_cache = caches
        refresher = BackgroundRefresher(feed_cache, article_cache, global_config)

        with patch.object(refresher, "_run_loop", new_callable=AsyncMock):
            await refresher.start()
            assert refresher._task is not None
            await refresher.stop()
            assert refresher._task is None
```

- [ ] **Step 3: 创建test_afdian_caching.py**

测试爱发电路由在有/无 article_cache 时的行为差异。

```python
"""爱发电路由文章级缓存逻辑测试"""

import pytest
from unittest.mock import AsyncMock, patch, MagicMock

from RSSGen.core.cache import Cache
from RSSGen.routes.afdian import AfdianRoute


@pytest.fixture
def route():
    return AfdianRoute({"cookie": "test", "rate_limit": 0})


@pytest.fixture
def article_cache():
    return Cache(ttl=60)


class TestFetchWithCache:
    @pytest.mark.asyncio
    async def test_cache_hit_skips_api(self, route, article_cache):
        """缓存命中时不调用详情API"""
        # 预填充缓存
        await article_cache.set("article:afdian:post1", "<p>cached content</p>")

        mock_posts = [{"post_id": "post1", "title": "test", "publish_time": 1700000000, "pics": [], "user": {"name": "a"}}]

        with patch.object(route, "_get_author_id", new_callable=AsyncMock, return_value="uid1"), \
             patch.object(route, "_get_post_list", new_callable=AsyncMock, return_value=mock_posts), \
             patch.object(route, "_get_post_detail", new_callable=AsyncMock) as mock_detail:

            items = await route.fetch(article_cache=article_cache, path_params=["slug1"])

            mock_detail.assert_not_called()
            assert len(items) == 1
            assert items[0].content == "<p>cached content</p>"

    @pytest.mark.asyncio
    async def test_cache_miss_calls_api_and_stores(self, route, article_cache):
        """缓存未命中时调用API并写入缓存"""
        mock_posts = [{"post_id": "post2", "title": "test", "publish_time": 1700000000, "pics": [], "user": {"name": "a"}}]

        with patch.object(route, "_get_author_id", new_callable=AsyncMock, return_value="uid1"), \
             patch.object(route, "_get_post_list", new_callable=AsyncMock, return_value=mock_posts), \
             patch.object(route, "_get_post_detail", new_callable=AsyncMock, return_value="<p>fresh</p>"):

            items = await route.fetch(article_cache=article_cache, path_params=["slug1"])

            assert items[0].content == "<p>fresh</p>"
            # 验证已写入缓存
            cached = await article_cache.get("article:afdian:post2")
            assert cached == "<p>fresh</p>"

    @pytest.mark.asyncio
    async def test_no_cache_still_works(self, route):
        """不传article_cache时保持原有行为"""
        mock_posts = [{"post_id": "post3", "title": "test", "publish_time": 1700000000, "pics": [], "user": {"name": "a"}}]

        with patch.object(route, "_get_author_id", new_callable=AsyncMock, return_value="uid1"), \
             patch.object(route, "_get_post_list", new_callable=AsyncMock, return_value=mock_posts), \
             patch.object(route, "_get_post_detail", new_callable=AsyncMock, return_value="<p>detail</p>"):

            items = await route.fetch(path_params=["slug1"])

            assert items[0].content == "<p>detail</p>"
```

- [ ] **Step 4: 运行测试**

Run: `pytest tests/ -v`
Expected: 全部通过

- [ ] **Step 5: 提交测试**

### Task 7: 更新文档

**Files:**
- Modify: `config.example.yml`（已在 Task 1 完成）
- Modify: `CLAUDE.md`（如果架构描述需要更新）

- [ ] **Step 1: 检查CLAUDE.md是否需要更新**

检查 CLAUDE.md 中的「请求流程」和「核心模块」部分，补充 `refresher.py` 和双层缓存的描述。

- [ ] **Step 2: 更新CLAUDE.md**

在核心模块列表中添加：
```
- **core/refresher.py** — BackgroundRefresher 后台调度器（预热 + 定时刷新 + 动态触发）
```

在请求流程中更新为读写分离架构描述。

- [ ] **Step 3: 提交文档更新**

### Task 8: 集成验证

- [ ] **Step 1: 启动服务验证基本流程**

Run: `timeout 10 uvicorn RSSGen.app:app --host 0.0.0.0 --port 8000 || true`
Expected: 服务启动无报错，日志显示 BackgroundRefresher 已启动

- [ ] **Step 2: 验证/status端点**

Run: `curl -s http://localhost:8000/status | python -m json.tool`
Expected: 返回 JSON，显示 enabled 和 feeds 状态

- [ ] **Step 3: 验证/feed端点缓存未命中返回空feed**

Run: `curl -s http://localhost:8000/feed/afdian/test_slug | head -5`
Expected: 返回合法的 Atom XML（空 feed）

## Self-Review

**Spec coverage:**
- [x] 双层缓存（FeedCache + ArticleCache） — Task 5
- [x] BackgroundRefresher后台调度器 — Task 3
- [x] Route.fetch()支持article_cache参数 — Task 2, 4
- [x] app.py读写分离改造 — Task 5
- [x] 配置扩展 — Task 1
- [x] /status端点 — Task 5
- [x] 错误处理与容错 — Task 3 中 _refresh_one 的异常处理
- [x] 空feed生成 — Task 5 中调用 generate_feed(info, [])
- [x] 测试 — Task 6
