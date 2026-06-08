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
        self.disabled_routes: set[str] = set()
        self._apprise = None

    def is_business_error(self, status_code: int) -> bool:
        """判断是否为业务错误"""
        return status_code in self.business_error_codes

    def is_route_disabled(self, route_key: str) -> bool:
        """检查路由是否被禁用"""
        return route_key in self.disabled_routes

    def disable_route(self, route_key: str):
        """禁用路由"""
        self.disabled_routes.add(route_key)

    async def notify(self, route_key: str, status_code: int, error_message: str):
        """发送通知"""
        if not self.enabled or not self.service_urls:
            return

        message = f"[RSSGen] 路由 {route_key} 获取失败\n"
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

            # apprise.notify 是同步方法，需要在事件循环中运行
            import asyncio

            loop = asyncio.get_event_loop()
            result = await loop.run_in_executor(None, self._apprise.notify, message)

            if result:
                logger.info("已发送通知")
            else:
                logger.error("发送通知失败：Apprise 返回 False")
        except Exception as e:
            logger.error(f"发送通知失败: {e}")
