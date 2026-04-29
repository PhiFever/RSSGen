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


# ── build_cache_key ───────────────────────────────────────

class TestBuildCacheKey:
    def test_basic(self):
        key = BackgroundRefresher.build_cache_key("afdian", ["author1"])
        assert key == "afdian/author1"

    def test_multiple_path_params(self):
        key = BackgroundRefresher.build_cache_key("afdian", ["a", "b"])
        assert key == "afdian/a/b"


# ── trigger ───────────────────────────────────────────────

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
                "afdian", ["author1"], fetch_kwargs={"limit": 5}
            )


# ── _refresh_one ──────────────────────────────────────────

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


# ── start / stop ──────────────────────────────────────────

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
                "afdian", ["author1"], fetch_kwargs={"limit": 5}
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

        refresher._error_status = {
            "afdian/author1": {"last_success": "2026-01-01T00:00:00", "error": None, "item_count": 5},
            "zhihu/user1": {"last_success": "2026-01-01T00:00:00", "error": None, "item_count": 3},
        }

        status = refresher.get_status()
        assert "afdian" in status
        assert "zhihu" in status
        assert "author1" in status["afdian"]
        assert "user1" in status["zhihu"]


# ── fetch_kwargs 透传 ─────────────────────────────────────

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
            call_kwargs = mock.call_args.kwargs["fetch_kwargs"]
            assert call_kwargs["limit"] == 15
            assert call_kwargs["custom_param"] == "value"
            assert "user_id" not in call_kwargs  # feed_id_field 不透传
            assert "alias" not in call_kwargs  # alias 不透传
