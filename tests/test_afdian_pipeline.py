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
