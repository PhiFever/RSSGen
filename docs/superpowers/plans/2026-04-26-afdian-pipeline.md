# afdian list/detail 流水线化实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 afdian 路由的 list 翻页和 detail 拉取做成流水线——每翻完一页 list 就立即派出该页的 detail 任务，让已下载内容尽快落到 SQLite store；并在 list / detail 任意环节出错时保留已落库的部分成果。

**Architecture:** 把 `_get_post_list` 改写为 async generator `_iter_post_list`，`fetch` 用 `async for` 消费每一页，对每个 post 用 `asyncio.create_task` 派一个 `_fetch_one_content`（查 store → miss 则下 detail → save）。最后 `asyncio.gather(*tasks, return_exceptions=True)` 收齐结果，按原始顺序装配 FeedItem，失败的跳过。全局 `Scraper._rate_limit_wait` 保持串行限流，风控强度不变。

**Tech Stack:** Python 3.12 / asyncio（标准库 create_task + gather）/ pytest + pytest-asyncio

**Spec:** `docs/superpowers/specs/2026-04-26-afdian-pipeline-design.md`

---

## File Map

| 路径 | 动作 | 责任 |
|---|---|---|
| `RSSGen/routes/afdian.py` | 修改 | `_get_post_list` → `_iter_post_list` 异步生成器；新增 `_fetch_one_content` 和 `_make_feed_item`；重写 `fetch` 用 create_task + gather |
| `tests/test_afdian_caching.py` | 修改 | mock 的 patch target 从 `_get_post_list` 改为 `_iter_post_list`，并改成异步生成器风格；语义不变 |
| `tests/test_afdian_pipeline.py` | 新建 | 三个场景：全成功、部分 detail 失败、list 中途失败 |

---

## Task 1: 基线校验

**Files:** 无改动；仅运行验证当前状态。

- [ ] **Step 1: 同步依赖**

```bash
uv sync
```

期望：环境就绪，无新依赖被装。

- [ ] **Step 2: 基线全量测试**

```bash
uv run pytest -q
```

期望：全部通过（含 `test_afdian_caching.py`、`test_refresher.py`、`test_article_store.py`）。如有失败，先修复再继续。

---

## Task 2: 把 `_get_post_list` 重构为 `_iter_post_list` 异步生成器

**Files:**
- Modify: `RSSGen/routes/afdian.py:54-92`（替换 `_get_post_list`）
- Modify: `RSSGen/routes/afdian.py:128`（fetch 调用点）
- Modify: `tests/test_afdian_caching.py`（mock 改异步生成器）

本任务保持串行 detail 拉取，仅把列表函数改成生成器并相应更新 fetch 主循环和测试。

- [ ] **Step 1: 替换 `_get_post_list` 为 `_iter_post_list`**

打开 `RSSGen/routes/afdian.py`：

第 1 行 imports 段确认有 `from typing import AsyncIterator`，没有则加上。当前文件第 1-8 行 import 段调整为：

```python
"""爱发电路由 — 参考 AfdianToMarkdown Go 项目的 API 端点"""

import asyncio
from datetime import datetime, timezone
from typing import AsyncIterator

from loguru import logger

from RSSGen.core.route import FeedInfo, FeedItem, Route
from RSSGen.core.scraper import Scraper
```

（`asyncio` 在 Task 4 才用，但提前加进来便于一次性提交导入。）

第 54-92 行 `_get_post_list` 整段替换为：

```python
    async def _iter_post_list(
        self, scraper: Scraper, user_id: str, author_slug: str,
        per_page: int = 10, limit: int = 0,
    ) -> AsyncIterator[list[dict]]:
        """逐页 yield 作者动态列表。limit=0 表示获取全部。"""
        referer = f"{HOST_URL}/a/{author_slug}"
        publish_sn = ""
        page = 1
        total_yielded = 0

        while True:
            api_url = (
                f"{HOST_URL}/api/post/get-list?"
                f"user_id={user_id}&type=old&publish_sn={publish_sn}"
                f"&per_page={per_page}&group_id=&all=1&is_public=&plan_id=&title=&name="
            )
            resp = await scraper.get(api_url, referer=referer)
            resp.raise_for_status()
            data = resp.json()
            self._check_api_response(data, f"get-list/{author_slug}")

            post_list = data.get("data", {}).get("list", [])
            if not post_list:
                logger.info(f"列表页 {page}: 无更多数据，结束翻页")
                return

            # limit 截断：本页加进去会超过 limit，只 yield 前 N 条
            if limit and total_yielded + len(post_list) >= limit:
                chunk = post_list[:limit - total_yielded]
                total_yielded += len(chunk)
                logger.info(f"列表页 {page}: 获取 {len(chunk)} 条，累计 {total_yielded} 条")
                yield chunk
                logger.info(f"已达 limit={limit}，停止翻页")
                return

            total_yielded += len(post_list)
            logger.info(f"列表页 {page}: 获取 {len(post_list)} 条，累计 {total_yielded} 条")
            yield post_list

            publish_sn = post_list[-1].get("publish_sn", "")
            if not publish_sn:
                logger.info(f"列表页 {page}: 无 publish_sn，结束翻页")
                return

            page += 1
```

- [ ] **Step 2: 更新 `fetch` 调用点为 `async for`（保持串行 detail）**

在 `RSSGen/routes/afdian.py` 中，把第 128-160 行 `fetch` 内"调用 `_get_post_list` + 循环 posts 取 detail"那一段（即 `posts = await self._get_post_list(...)` 起到 `items.append(FeedItem(...))` 结束的整个块）替换为：

```python
        items: list[FeedItem] = []
        async for page in self._iter_post_list(scraper, user_id, author_slug, limit=limit):
            for post in page:
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
```

注意 fetch 末尾的 `logger.info(f"抓取完成 ...")` 行不变。

- [ ] **Step 3: 更新 `tests/test_afdian_caching.py` mocks**

把 `tests/test_afdian_caching.py` 整个文件替换为：

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


def _iter_pages(pages):
    """返回一个调用即得 async generator 的函数，依次 yield 每一页。"""
    async def _gen(*args, **kwargs):
        for page in pages:
            yield page
    return _gen


class TestFetchWithStore:
    @pytest.mark.asyncio
    async def test_store_hit_skips_api(self, route, article_store):
        """store 命中时不调用详情 API"""
        await article_store.save("afdian", "post1", "<p>cached content</p>")
        mock_pages = [[_make_post("post1")]]

        with patch.object(route, "_get_author_id", new_callable=AsyncMock, return_value="uid1"), \
             patch.object(route, "_iter_post_list", new=_iter_pages(mock_pages)), \
             patch.object(route, "_get_post_detail", new_callable=AsyncMock) as mock_detail:

            items = await route.fetch(article_store=article_store, path_params=["slug1"])

            mock_detail.assert_not_called()
            assert len(items) == 1
            assert items[0].content == "<p>cached content</p>"

    @pytest.mark.asyncio
    async def test_store_miss_calls_api_and_saves(self, route, article_store):
        """store 未命中时调用 API 并落库"""
        mock_pages = [[_make_post("post2")]]

        with patch.object(route, "_get_author_id", new_callable=AsyncMock, return_value="uid1"), \
             patch.object(route, "_iter_post_list", new=_iter_pages(mock_pages)), \
             patch.object(route, "_get_post_detail", new_callable=AsyncMock, return_value="<p>fresh</p>"):

            items = await route.fetch(article_store=article_store, path_params=["slug1"])

            assert items[0].content == "<p>fresh</p>"
            saved = await article_store.get("afdian", "post2")
            assert saved == "<p>fresh</p>"

    @pytest.mark.asyncio
    async def test_no_store_still_works(self, route):
        """不传 article_store 时仍能正常走 API"""
        mock_pages = [[_make_post("post3")]]

        with patch.object(route, "_get_author_id", new_callable=AsyncMock, return_value="uid1"), \
             patch.object(route, "_iter_post_list", new=_iter_pages(mock_pages)), \
             patch.object(route, "_get_post_detail", new_callable=AsyncMock, return_value="<p>detail</p>"):

            items = await route.fetch(path_params=["slug1"])

            assert items[0].content == "<p>detail</p>"
```

- [ ] **Step 4: 跑测试确认通过**

```bash
uv run pytest tests/test_afdian_caching.py -v
```

期望：3 个测试全部通过。

```bash
uv run pytest -q
```

期望：全量测试通过。

- [ ] **Step 5: 提交**

```bash
git add RSSGen/routes/afdian.py tests/test_afdian_caching.py
git commit -m "refactor: 将 _get_post_list 改写为 _iter_post_list 异步生成器"
```

---

## Task 3: 抽出 `_fetch_one_content` 和 `_make_feed_item` 辅助方法

纯重构。`fetch` 主循环更瘦，行为完全不变。

**Files:**
- Modify: `RSSGen/routes/afdian.py`（fetch 内联代码抽到两个新方法）

- [ ] **Step 1: 在 `fetch` 之前新增两个辅助方法**

打开 `RSSGen/routes/afdian.py`，在 `async def fetch(...)` 那一行之前（即 `async def feed_info(...)` 之后）插入：

```python
    async def _fetch_one_content(
        self, scraper: Scraper, article_store, post: dict
    ) -> str:
        """单篇文章内容获取：先查 store，未命中则下 detail 并落库。"""
        post_id = post.get("post_id", "")
        if article_store:
            cached = await article_store.get("afdian", post_id)
            if cached is not None:
                return cached

        content = await self._get_post_detail(scraper, post_id)
        logger.info(f"文章详情下载成功: {post.get('title', post_id)}")
        if article_store and content:
            await article_store.save("afdian", post_id, content)
        return content

    def _make_feed_item(self, post: dict, content: str) -> FeedItem:
        """根据 post dict 与正文 content 构造 FeedItem。"""
        publish_time = int(post.get("publish_time", 0))
        pub_date = datetime.fromtimestamp(publish_time, tz=timezone.utc) if publish_time else None

        enclosures = []
        for pic in post.get("pics", []):
            if pic:
                enclosures.append({"url": pic, "type": "image/jpeg"})

        post_id = post.get("post_id", "")
        return FeedItem(
            title=post.get("title", "无标题"),
            link=f"{HOST_URL}/p/{post_id}",
            content=content or "",
            pub_date=pub_date,
            author=post.get("user", {}).get("name"),
            guid=post_id,
            enclosures=enclosures,
        )
```

- [ ] **Step 2: 用两个新方法重写 fetch 内 `async for` 循环体**

把上一任务 Step 2 中那段 `async for page in self._iter_post_list(...)` 整段（直到末尾 `items.append(FeedItem(...))` 闭合处）替换为：

```python
        items: list[FeedItem] = []
        async for page in self._iter_post_list(scraper, user_id, author_slug, limit=limit):
            for post in page:
                content = await self._fetch_one_content(scraper, article_store, post)
                items.append(self._make_feed_item(post, content))
```

- [ ] **Step 3: 跑测试确认通过**

```bash
uv run pytest -q
```

期望：全量测试通过——纯重构，行为完全一致。

- [ ] **Step 4: 提交**

```bash
git add RSSGen/routes/afdian.py
git commit -m "refactor: 抽出 _fetch_one_content 和 _make_feed_item 辅助方法"
```

---

## Task 4: 新增 `tests/test_afdian_pipeline.py`（TDD 红灯阶段）

**Files:**
- Create: `tests/test_afdian_pipeline.py`

写完后预期：场景 1 通过（当前串行实现也能满足），场景 2、3 失败（需 Task 5 实现流水线后才通过）。

- [ ] **Step 1: 创建测试文件**

创建 `tests/test_afdian_pipeline.py`：

```python
"""afdian 路由 list/detail 流水线行为测试"""

import asyncio

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
    s = SqliteArticleStore(tmp_path / "pipeline.db")
    await s.init()
    yield s
    await s.close()


def _make_post(post_id: str):
    return {
        "post_id": post_id,
        "title": f"title-{post_id}",
        "publish_time": 1700000000,
        "pics": [],
        "user": {"name": "a"},
    }


def _iter_pages(pages):
    async def _gen(*args, **kwargs):
        for page in pages:
            yield page
    return _gen


def _iter_pages_then_raise(pages, exc):
    """yield 完所有 pages 后 raise exc——模拟 list 翻页中途失败。"""
    async def _gen(*args, **kwargs):
        for page in pages:
            yield page
        raise exc
    return _gen


class TestPipeline:
    @pytest.mark.asyncio
    async def test_all_success_preserves_order_and_count(self, route, article_store):
        """所有 detail 成功 — 顺序与数量正确"""
        pages = [
            [_make_post("p1"), _make_post("p2"), _make_post("p3")],
            [_make_post("p4"), _make_post("p5")],
        ]

        async def detail_mock(scraper, post_id, album_id=""):
            return f"<p>{post_id}</p>"

        with patch.object(route, "_get_author_id", new_callable=AsyncMock, return_value="uid"), \
             patch.object(route, "_iter_post_list", new=_iter_pages(pages)), \
             patch.object(route, "_get_post_detail", side_effect=detail_mock):

            items = await route.fetch(article_store=article_store, path_params=["slug"])

        assert len(items) == 5
        assert [i.guid for i in items] == ["p1", "p2", "p3", "p4", "p5"]
        for item in items:
            assert item.content == f"<p>{item.guid}</p>"

    @pytest.mark.asyncio
    async def test_partial_detail_failure_drops_failed_keeps_rest(self, route, article_store):
        """部分 detail 失败 — 失败的丢弃，成功的保留并落库"""
        pages = [
            [_make_post("p1"), _make_post("p2"), _make_post("p3"), _make_post("p4")],
        ]

        async def detail_mock(scraper, post_id, album_id=""):
            if post_id == "p3":
                raise RuntimeError("simulated detail failure")
            return f"<p>{post_id}</p>"

        with patch.object(route, "_get_author_id", new_callable=AsyncMock, return_value="uid"), \
             patch.object(route, "_iter_post_list", new=_iter_pages(pages)), \
             patch.object(route, "_get_post_detail", side_effect=detail_mock):

            items = await route.fetch(article_store=article_store, path_params=["slug"])

        assert len(items) == 3
        assert [i.guid for i in items] == ["p1", "p2", "p4"]

        # 成功的 3 篇都已落库
        for post_id in ("p1", "p2", "p4"):
            assert await article_store.get("afdian", post_id) == f"<p>{post_id}</p>"
        # 失败的没落库
        assert await article_store.get("afdian", "p3") is None

    @pytest.mark.asyncio
    async def test_list_pagination_failure_preserves_in_flight_saves(self, route, article_store):
        """list 翻页中途失败 — 已派出的 detail task 仍要等它们完成并落库"""
        pages = [
            [_make_post("p1"), _make_post("p2"), _make_post("p3")],
        ]

        async def slow_detail(scraper, post_id, album_id=""):
            # 模拟有耗时的 detail 请求；如果 fetch 不等待已派任务就 re-raise，
            # 测试结束时 store 里就没有这 3 条
            await asyncio.sleep(0.05)
            return f"<p>{post_id}</p>"

        with patch.object(route, "_get_author_id", new_callable=AsyncMock, return_value="uid"), \
             patch.object(route, "_iter_post_list",
                          new=_iter_pages_then_raise(pages, RuntimeError("list boom"))), \
             patch.object(route, "_get_post_detail", side_effect=slow_detail):

            with pytest.raises(RuntimeError, match="list boom"):
                await route.fetch(article_store=article_store, path_params=["slug"])

        # 关键断言：fetch 抛 RuntimeError 之前，已派出的 3 个 detail 必须全部落库
        for post_id in ("p1", "p2", "p3"):
            assert await article_store.get("afdian", post_id) == f"<p>{post_id}</p>"
```

- [ ] **Step 2: 跑测试看红灯**

```bash
uv run pytest tests/test_afdian_pipeline.py -v
```

期望：
- `test_all_success_preserves_order_and_count` — **PASS**（当前串行实现也能跑通）
- `test_partial_detail_failure_drops_failed_keeps_rest` — **FAIL**（当前 fetch 在 p3 detail 抛 RuntimeError 时直接上抛，没有 isinstance/skip 逻辑；测试报错 `RuntimeError: simulated detail failure`）
- `test_list_pagination_failure_preserves_in_flight_saves` — **PASS**（巧合通过：当前串行 fetch 在 `async for` 取下一页之前已经把当前页所有 post 都同步处理并落库；这跟 Task 5 想要保证的"并发派任务后失败仍要等已派的跑完"是同一外在结果，但内部机制不同。Task 5 实施完后此测试转为对 try/except + gather 兜底逻辑的回归保护——若实施者漏写 try/except，此测试会**新失败**）

确认 scenario 2 是真正的红灯——这是 TDD 红灯阶段，等 Task 5 实现完转绿。

- [ ] **Step 3: 提交**

```bash
git add tests/test_afdian_pipeline.py
git commit -m "test: 新增 afdian 流水线行为测试（红灯 - 待实现）"
```

---

## Task 5: 在 `fetch` 中实施流水线（TDD 绿灯）

**Files:**
- Modify: `RSSGen/routes/afdian.py:fetch`（重写主循环为 create_task + gather）

- [ ] **Step 1: 重写 `fetch` 主循环**

打开 `RSSGen/routes/afdian.py`，把 `fetch` 方法整段（从 `async def fetch(self, article_store=None, **kwargs) -> list[FeedItem]:` 开始到该方法结束）替换为：

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

        posts: list[dict] = []
        tasks: list[asyncio.Task[str]] = []

        try:
            async for page in self._iter_post_list(scraper, user_id, author_slug, limit=limit):
                for post in page:
                    posts.append(post)
                    tasks.append(asyncio.create_task(
                        self._fetch_one_content(scraper, article_store, post)
                    ))
        except asyncio.CancelledError:
            for t in tasks:
                if not t.done():
                    t.cancel()
            raise
        except Exception:
            # list 翻页失败 - 但仍等已派的 detail task 完成（让 save 落地）
            if tasks:
                await asyncio.gather(*tasks, return_exceptions=True)
            raise

        try:
            contents = await asyncio.gather(*tasks, return_exceptions=True)
        except asyncio.CancelledError:
            for t in tasks:
                if not t.done():
                    t.cancel()
            raise

        items: list[FeedItem] = []
        for post, content in zip(posts, contents):
            post_id = post.get("post_id", "")
            if isinstance(content, Exception):
                logger.warning(f"文章详情获取失败，跳过 {post_id}: {content}")
                continue
            items.append(self._make_feed_item(post, content))

        logger.info(f"抓取完成 {author_slug}: {len(items)}/{len(posts)} 条文章")
        return items
```

- [ ] **Step 2: 跑流水线测试确认全绿**

```bash
uv run pytest tests/test_afdian_pipeline.py -v
```

期望：3 个场景全部 PASS。

- [ ] **Step 3: 跑 caching 测试确认契约未破坏**

```bash
uv run pytest tests/test_afdian_caching.py -v
```

期望：3 个测试仍全部 PASS（store hit/miss/no-store 行为在并发后仍成立）。

- [ ] **Step 4: 跑全量测试**

```bash
uv run pytest -q
```

期望：全部通过。

- [ ] **Step 5: 提交**

```bash
git add RSSGen/routes/afdian.py
git commit -m "feat: afdian fetch 改用 create_task + gather 实现 list/detail 流水线"
```

---

## Task 6: 烟测（手动验证日志格式）

**Files:** 无代码改动；运行 + 观察日志。

只需要快速确认新日志格式（"抓取完成 ...: N/M 条文章"）正常出现，pipeline 不会在某些边界条件下卡死。

- [ ] **Step 1: 跑一个全 mock 的端到端 fetch**

```bash
uv run python -c "
import asyncio
from unittest.mock import AsyncMock, patch
from RSSGen.routes.afdian import AfdianRoute

async def fake_iter(*a, **k):
    yield [{'post_id': 'a', 'title': 'A', 'publish_time': 1700000000, 'pics': [], 'user': {'name': 'u'}}]
    yield [{'post_id': 'b', 'title': 'B', 'publish_time': 1700000001, 'pics': [], 'user': {'name': 'u'}}]

async def detail(scraper, post_id, album_id=''):
    if post_id == 'b':
        raise RuntimeError('boom')
    return f'<p>{post_id}</p>'

async def main():
    route = AfdianRoute({'cookie': '', 'rate_limit': 0})
    with patch.object(route, '_get_author_id', new_callable=AsyncMock, return_value='u'), \
         patch.object(route, '_iter_post_list', new=fake_iter), \
         patch.object(route, '_get_post_detail', side_effect=detail):
        items = await route.fetch(path_params=['demo'])
    print(f'最终 items: {[i.guid for i in items]}')

asyncio.run(main())
"
```

期望输出包含：
- `开始抓取 demo，limit=20`
- `列表页 1: 获取 1 条，累计 1 条`
- `列表页 2: 获取 1 条，累计 2 条`
- `文章详情下载成功: A`
- `文章详情获取失败，跳过 b: boom`（warning 级）
- `抓取完成 demo: 1/2 条文章`
- `最终 items: ['a']`

- [ ] **Step 2: 最终全量测试**

```bash
uv run pytest -q
```

期望：全部通过。

- [ ] **Step 3: 确认本任务无未提交改动**

```bash
git status
```

期望：无未提交的代码改动。

---

## 完成标准

- [ ] `uv run pytest -q` 全绿
- [ ] `tests/test_afdian_pipeline.py` 三个场景全部通过
- [ ] `tests/test_afdian_caching.py` 三个场景全部通过（mock 改用 `_iter_post_list` 后语义保持）
- [ ] 烟测脚本输出新版日志格式（含 "成功数/总数"）
- [ ] `git log --oneline` 显示 4 个提交：`refactor: 异步生成器`、`refactor: 抽出辅助方法`、`test: 红灯`、`feat: 流水线`
