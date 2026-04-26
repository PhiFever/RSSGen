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
