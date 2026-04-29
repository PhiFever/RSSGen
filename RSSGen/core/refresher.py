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
                feed_id_field = self._get_feed_id_field(route_name)
                for key, value in feed_conf.items():
                    if key != feed_id_field and key != "alias" and key not in fetch_kwargs:
                        fetch_kwargs[key] = value

        asyncio.create_task(
            self._refresh_one(route_name, path_params, fetch_kwargs=fetch_kwargs)
        )

    def _get_feed_id_field(self, route_name: str) -> str:
        route_cls = get_registry().get(route_name)
        return getattr(route_cls, "feed_id_field", "user_id") if route_cls else "user_id"

    def _find_feed_config(self, route_name: str, feed_id: str) -> dict | None:
        feeds = self.config.get("routes", {}).get(route_name, {}).get("feeds", [])
        feed_id_field = self._get_feed_id_field(route_name)
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

        feed_id_field = self._get_feed_id_field(route_name)

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
