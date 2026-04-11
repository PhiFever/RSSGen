"""后台调度器：负责预热和定期刷新feed缓存"""

import asyncio
import logging
from datetime import datetime, timezone

from RSSGen.core.cache import Cache

logger = logging.getLogger("rssgen.refresher")

# 默认配置
DEFAULT_STARTUP_DELAY = 5      # 启动延迟（秒），等待网络稳定
DEFAULT_MAX_RETRIES = 3        # 最大重试次数
DEFAULT_RETRY_BASE_DELAY = 5   # 重试基础延迟（秒）


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

        # 可配置的刷新参数
        refresher_config = config.get("refresher", {})
        self.startup_delay = refresher_config.get("startup_delay", DEFAULT_STARTUP_DELAY)
        self.max_retries = refresher_config.get("max_retries", DEFAULT_MAX_RETRIES)
        self.retry_base_delay = refresher_config.get("retry_base_delay", DEFAULT_RETRY_BASE_DELAY)

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

    async def trigger(self, route_name: str, path_params: list[str],
                      query_params: dict | None = None):
        """动态触发：未知feed首次访问时调用，非阻塞"""
        cache_key = self.build_cache_key(route_name, path_params)
        if cache_key not in self._pending:
            asyncio.create_task(self._refresh_one(route_name, path_params,
                                                  fetch_kwargs=query_params))

    def get_status(self) -> dict:
        """返回所有feed的刷新状态"""
        return self._error_status.copy()

    # ---- 内部方法 ----

    @staticmethod
    def build_cache_key(route_name: str, path_params: list[str]) -> str:
        path = "/".join(path_params)
        return f"{route_name}/{path}"

    async def _preinit_curl_cffi(self):
        """预初始化 curl_cffi 库，确保在 uvicorn 环境中正常工作"""
        from curl_cffi.requests import AsyncSession
        from curl_cffi.const import CurlOpt

        # 使用一个简单的请求来初始化底层 libcurl 库
        # 这可以解决 curl_cffi 在 uvicorn/uvloop 启动阶段的异步兼容问题
        try:
            async with AsyncSession(
                impersonate="chrome131",
                curl_options={CurlOpt.FRESH_CONNECT: True},
            ) as session:
                # 连接一个可靠的、响应快速的测试端点
                resp = await session.get("https://httpbin.org/get", timeout=10)
                if resp.status_code == 200:
                    logger.info("HTTP 客户端预初始化成功")
                else:
                    logger.warning(f"HTTP 客户端预初始化响应异常: {resp.status_code}")
        except Exception as e:
            logger.warning(f"HTTP 客户端预初始化失败: {e}，将继续尝试正常请求")

    async def _run_loop(self):
        """主循环：预热 + 定时刷新"""
        try:
            # 启动延迟：等待网络稳定
            logger.info(f"等待 {self.startup_delay} 秒确保网络就绪...")
            await asyncio.sleep(self.startup_delay)

            # 预初始化 curl_cffi：强制底层 libcurl 库在 uvicorn 环境中正确加载
            # 解决 curl_cffi 在 uvicorn/uvloop 启动阶段的异步兼容问题
            logger.info("预初始化 HTTP 客户端...")
            await self._preinit_curl_cffi()

            await self._warmup()

            # 从第一个启用了 feeds 的路由获取 refresh_interval
            # 目前只有 afdian，后续扩展时可改为按路由独立调度
            afdian_config = self.config.get("routes", {}).get("afdian", {})
            refresh_interval = afdian_config.get("refresh_interval", 14400)

            while True:
                await asyncio.sleep(refresh_interval)
                try:
                    await self._refresh_all()
                except Exception:
                    logger.exception("定时刷新异常，将在下一轮重试")
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
                await self._refresh_one("afdian", [slug],
                                        fetch_kwargs={"limit": str(limit)})
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
                await self._refresh_one("afdian", [slug],
                                        fetch_kwargs={"limit": str(limit)})
        logger.info("定时刷新完成")

    async def _refresh_one(self, route_name: str, path_params: list[str],
                           fetch_kwargs: dict | None = None):
        """刷新单个feed，失败时自动重试

        参数:
            fetch_kwargs: 传给 route.fetch() 的额外参数（如 limit）
        """
        from RSSGen.core.feed import generate_feed
        from RSSGen.routes import get_registry

        cache_key = self.build_cache_key(route_name, path_params)

        if cache_key in self._pending:
            return

        self._pending.add(cache_key)

        try:
            # 重试循环
            last_error = None
            for attempt in range(self.max_retries):
                try:
                    if attempt > 0:
                        # 指数退避：第2次等5秒，第3次等10秒...
                        delay = self.retry_base_delay * (2 ** (attempt - 1))
                        logger.info(f"重试 {cache_key} (第{attempt + 1}次)，等待 {delay} 秒...")
                        await asyncio.sleep(delay)
                    else:
                        logger.info(f"正在刷新 {cache_key}")

                    registry = get_registry()
                    route_cls = registry.get(route_name)
                    if not route_cls:
                        raise ValueError(f"路由不存在: {route_name}")

                    # 合并全局 scraper 配置到路由配置（路由配置优先，全局配置作为 fallback）
                    route_config = self.config.get("routes", {}).get(route_name, {})
                    global_scraper = self.config.get("scraper", {})
                    merged_config = {**global_scraper, **route_config}
                    route = route_cls(merged_config)

                    kwargs = dict(fetch_kwargs or {})
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
                    return  # 成功，退出

                except Exception as e:
                    last_error = e
                    logger.warning(f"刷新失败 {cache_key} (第{attempt + 1}次): {e}")

            # 所有重试都失败
            logger.error(f"刷新失败 {cache_key}: 所有 {self.max_retries} 次重试均失败")
            self._error_status[cache_key] = {
                "last_success": self._error_status.get(cache_key, {}).get("last_success"),
                "error": str(last_error),
                "item_count": 0,
            }

        finally:
            self._pending.discard(cache_key)
