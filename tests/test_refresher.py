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
        key = BackgroundRefresher.build_cache_key("afdian", ["author1"])
        assert key == "afdian/author1"

    def test_multiple_path_params(self):
        key = BackgroundRefresher.build_cache_key("afdian", ["a", "b"])
        assert key == "afdian/a/b"


class TestTrigger:
    @pytest.mark.asyncio
    async def test_trigger_creates_task(self, caches, global_config):
        feed_cache, article_cache = caches
        refresher = BackgroundRefresher(feed_cache, article_cache, global_config)

        with patch.object(refresher, "_refresh_one", new_callable=AsyncMock) as mock_refresh:
            await refresher.trigger("afdian", ["author1"], {"limit": "10"})
            await asyncio.sleep(0.1)  # 让 create_task 有机会执行
            mock_refresh.assert_called_once_with("afdian", ["author1"],
                                                 fetch_kwargs={"limit": "10"})

    @pytest.mark.asyncio
    async def test_trigger_dedup(self, caches, global_config):
        """已在刷新中的feed不会重复触发"""
        feed_cache, article_cache = caches
        refresher = BackgroundRefresher(feed_cache, article_cache, global_config)

        cache_key = BackgroundRefresher.build_cache_key("afdian", ["author1"])
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
            await refresher._refresh_one("afdian", ["author1"],
                                         fetch_kwargs={"limit": "5"})

        cache_key = "afdian/author1"
        assert cache_key in refresher._error_status
        assert refresher._error_status[cache_key]["error"] is None
        assert cache_key not in refresher._pending  # finally 中已移除

    @pytest.mark.asyncio
    async def test_failure_records_error(self, caches, global_config):
        feed_cache, article_cache = caches
        refresher = BackgroundRefresher(feed_cache, article_cache, global_config)

        mock_registry = {"afdian": MagicMock(side_effect=RuntimeError("boom"))}

        with patch("RSSGen.core.refresher.get_registry", return_value=mock_registry):
            await refresher._refresh_one("afdian", ["author1"],
                                         fetch_kwargs={"limit": "5"})

        cache_key = "afdian/author1"
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
