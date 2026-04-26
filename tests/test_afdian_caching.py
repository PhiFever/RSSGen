"""爱发电路由文章级持久化逻辑测试"""

import pytest
from unittest.mock import AsyncMock, patch

from tests.helpers import _iter_pages, _make_post


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
