"""通知模块：当获取上游数据失败时发送通知"""

from datetime import datetime

from loguru import logger


class Notifier:
    """通知管理器"""

    # 默认业务错误状态码
    DEFAULT_BUSINESS_ERROR_CODES = [400, 401, 403, 404, 410, 422, 451]

    def __init__(self, config: dict):
        """初始化 notifier，加载配置"""
        notifier_config = config.get("notifier", {})
        self.enabled = notifier_config.get("enabled", False)
        self.service_urls = notifier_config.get("service_urls", [])
        self.business_error_codes = set(
            notifier_config.get("business_error_codes", self.DEFAULT_BUSINESS_ERROR_CODES)
        )
        self.disabled_feeds: set[str] = set()
        self._apprise = None

    def is_business_error(self, status_code: int) -> bool:
        """判断是否为业务错误"""
        return status_code in self.business_error_codes

    def is_feed_disabled(self, feed_key: str) -> bool:
        """检查 feed 是否被禁用（feed_key 即 cache_key：route/feed_id）"""
        return feed_key in self.disabled_feeds

    def disable_feed(self, feed_key: str):
        """禁用单个 feed（feed_key 即 cache_key：route/feed_id）"""
        self.disabled_feeds.add(feed_key)

    async def notify(self, feed_key: str, status_code: int, error_message: str):
        """发送通知"""
        if not self.enabled or not self.service_urls:
            return

        message = f"[RSSGen] 订阅源 {feed_key} 获取失败\n"
        message += f"状态码: {status_code}\n"
        message += f"错误: {error_message}\n"
        message += f"时间: {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}"

        await self._send_notification(message)

    async def _send_notification(self, message: str):
        """实际发送通知的内部方法"""
        try:
            if self._apprise is None:
                import apprise

                self._apprise = apprise.Apprise()
                for url in self.service_urls:
                    self._apprise.add(url)

            # apprise.notify 是同步阻塞方法，放到线程池执行避免阻塞事件循环
            import asyncio

            result = await asyncio.to_thread(self._apprise.notify, message)

            if result:
                logger.info("已发送通知")
            else:
                logger.error("发送通知失败：Apprise 返回 False")
        except Exception as e:
            logger.error(f"发送通知失败: {e}")
