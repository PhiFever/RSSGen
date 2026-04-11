"""爱发电路由文章级缓存逻辑测试"""

import pytest
from unittest.mock import AsyncMock, patch

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