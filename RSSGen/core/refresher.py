"""后台调度器：负责预热和定期刷新feed缓存"""

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
        self._task: asyncio.Task | None = None
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

    async def start(self):
        if self._task is None:
            self._task = asyncio.create_task(self._run_loop())
            logger.info("BackgroundRefresher 已启动")

    async def stop(self):
        if self._task:
            self._task.cancel()
            try:
                await self._task
            except asyncio.CancelledError:
                pass
            self._task = None
            logger.info("BackgroundRefresher 已停止")

    async def trigger(
        self, route_name: str, path_params: list[str], query_params: dict | None = None
    ):
        """动态触发：未知feed首次访问时调用，非阻塞"""
        if self.build_cache_key(route_name, path_params) in self._pending:
            return

        fetch_kwargs = dict(query_params or {})
        # query_params 未显式指定时，从 feeds 配置中按 slug 补齐参数（如 limit），
        # 保证动态触发与定时刷新行为一致
        if path_params:
            feed_conf = self._find_feed_config(route_name, path_params[0])
            if feed_conf:
                for key, value in feed_conf.items():
                    if key != "slug" and key not in fetch_kwargs:
                        fetch_kwargs[key] = value

        asyncio.create_task(
            self._refresh_one(route_name, path_params, fetch_kwargs=fetch_kwargs)
        )

    def _find_feed_config(self, route_name: str, slug: str) -> dict | None:
        feeds = self.config.get("routes", {}).get(route_name, {}).get("feeds", [])
        for fc in feeds:
            if fc.get("slug") == slug:
                return fc
        return None

    def get_status(self) -> dict:
        return self._error_status.copy()

    @staticmethod
    def build_cache_key(route_name: str, path_params: list[str]) -> str:
        return f"{route_name}/{'/'.join(path_params)}"

    async def _preinit_curl_cffi(self):
        """对目标站点发起一次无害请求，强制底层 libcurl 在 uvicorn/uvloop 启动阶段正确加载"""
        try:
            async with AsyncSession(
                impersonate="chrome131",
                curl_options={CurlOpt.FRESH_CONNECT: True},
            ) as session:
                resp = await session.get("https://afdian.com/", timeout=10)
                if resp.status_code == 200:
                    logger.info("HTTP 客户端预初始化成功")
                else:
                    logger.warning(f"HTTP 客户端预初始化响应异常: {resp.status_code}")
        except Exception as e:
            logger.warning(f"HTTP 客户端预初始化失败: {e}，将继续尝试正常请求")

    async def _run_loop(self):
        try:
            logger.info(f"等待 {self.startup_delay} 秒确保网络就绪...")
            await asyncio.sleep(self.startup_delay)

            logger.info("预初始化 HTTP 客户端...")
            await self._preinit_curl_cffi()

            await self._refresh_feeds("预热")

            refresh_interval = (
                self.config.get("routes", {})
                .get("afdian", {})
                .get("refresh_interval", 14400)
            )

            while True:
                await asyncio.sleep(refresh_interval)
                try:
                    await self._refresh_feeds("定时刷新")
                except Exception:
                    logger.exception("定时刷新异常，将在下一轮重试")
        except asyncio.CancelledError:
            raise
        except Exception:
            logger.exception("BackgroundRefresher 主循环异常退出")

    async def _refresh_feeds(self, label: str):
        feeds = self.config.get("routes", {}).get("afdian", {}).get("feeds", [])
        if not feeds:
            logger.info(f"未配置 feed 列表，跳过{label}")
            return

        logger.info(f"开始{label} {len(feeds)} 个 feed")
        for feed_conf in feeds:
            slug = feed_conf.get("slug")
            if slug:
                await self._refresh_one(
                    "afdian", [slug], fetch_kwargs={"limit": feed_conf.get("limit", 20)}
                )
        logger.info(f"{label}完成")

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
                    logger.warning(f"刷新失败 {cache_key} (第{attempt + 1}次): {e}")

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
