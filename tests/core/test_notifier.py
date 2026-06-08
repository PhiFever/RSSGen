"""notifier 模块单元测试"""

import sys
from unittest.mock import MagicMock

import pytest

from RSSGen.core.notifier import Notifier


class TestNotifier:
    """Notifier 类测试"""

    def test_init_default(self):
        """测试默认初始化"""
        notifier = Notifier({})
        assert notifier.enabled is False
        assert notifier.service_urls == []
        assert notifier.business_error_codes == {400, 401, 403, 404, 410, 422, 451}
        assert notifier.disabled_routes == set()

    def test_init_with_config(self):
        """测试带配置初始化"""
        config = {
            "notifier": {
                "enabled": True,
                "service_urls": ["tgram://token/chat_id"],
                "business_error_codes": [403, 401],
            }
        }
        notifier = Notifier(config)
        assert notifier.enabled is True
        assert notifier.service_urls == ["tgram://token/chat_id"]
        assert notifier.business_error_codes == {403, 401}

    def test_is_business_error(self):
        """测试业务错误判断"""
        notifier = Notifier({})

        # 默认业务错误
        assert notifier.is_business_error(400) is True
        assert notifier.is_business_error(401) is True
        assert notifier.is_business_error(403) is True
        assert notifier.is_business_error(404) is True
        assert notifier.is_business_error(410) is True
        assert notifier.is_business_error(422) is True
        assert notifier.is_business_error(451) is True

        # 非业务错误
        assert notifier.is_business_error(200) is False
        assert notifier.is_business_error(500) is False
        assert notifier.is_business_error(502) is False
        assert notifier.is_business_error(503) is False

    def test_is_business_error_custom(self):
        """测试自定义业务错误状态码"""
        config = {
            "notifier": {
                "business_error_codes": [403, 429],
            }
        }
        notifier = Notifier(config)

        assert notifier.is_business_error(403) is True
        assert notifier.is_business_error(429) is True
        assert notifier.is_business_error(400) is False
        assert notifier.is_business_error(401) is False

    def test_is_route_disabled(self):
        """测试路由禁用检查"""
        notifier = Notifier({})

        # 初始状态
        assert notifier.is_route_disabled("afdian") is False
        assert notifier.is_route_disabled("zhihu") is False

    def test_disable_route(self):
        """测试禁用路由"""
        notifier = Notifier({})

        notifier.disable_route("afdian")
        assert notifier.is_route_disabled("afdian") is True
        assert notifier.is_route_disabled("zhihu") is False

        notifier.disable_route("zhihu")
        assert notifier.is_route_disabled("afdian") is True
        assert notifier.is_route_disabled("zhihu") is True

    def test_disable_route_idempotent(self):
        """测试禁用路由幂等性"""
        notifier = Notifier({})

        notifier.disable_route("afdian")
        notifier.disable_route("afdian")
        assert notifier.is_route_disabled("afdian") is True

    @pytest.mark.asyncio
    async def test_notify_disabled(self):
        """测试通知未启用时不发送"""
        notifier = Notifier({"notifier": {"enabled": False}})
        # 不应抛出异常
        await notifier.notify("afdian", 403, "Forbidden")

    @pytest.mark.asyncio
    async def test_notify_no_service_urls(self):
        """测试没有配置 service_urls 时不发送"""
        notifier = Notifier({"notifier": {"enabled": True, "service_urls": []}})
        # 不应抛出异常
        await notifier.notify("afdian", 403, "Forbidden")

    @pytest.mark.asyncio
    async def test_notify_with_mock_apprise(self, monkeypatch):
        """测试通知发送，验证消息内容包含路由、状态码和错误信息"""
        captured_messages = []

        def mock_notify(message):
            captured_messages.append(message)
            return True

        mock_apprise_module = MagicMock()
        mock_apprise_instance = MagicMock()
        mock_apprise_instance.notify = mock_notify
        mock_apprise_module.Apprise.return_value = mock_apprise_instance
        monkeypatch.setitem(sys.modules, "apprise", mock_apprise_module)

        config = {
            "notifier": {
                "enabled": True,
                "service_urls": ["tgram://token/chat_id"],
            }
        }
        notifier = Notifier(config)

        await notifier.notify("afdian", 403, "Forbidden")

        assert len(captured_messages) == 1
        message = captured_messages[0]
        assert "afdian" in message
        assert "403" in message
        assert "Forbidden" in message

    @pytest.mark.asyncio
    async def test_state_combination_smoke(self):
        """状态组合冒烟测试：业务错误判断 + 路由禁用的组合使用"""
        config = {
            "notifier": {
                "enabled": False,
                "service_urls": ["tgram://token/chat_id"],
            }
        }
        notifier = Notifier(config)

        # 业务错误判断
        assert notifier.is_business_error(403) is True
        assert notifier.is_business_error(500) is False

        # 路由禁用状态变化
        assert notifier.is_route_disabled("afdian") is False
        notifier.disable_route("afdian")
        assert notifier.is_route_disabled("afdian") is True
        assert notifier.is_route_disabled("zhihu") is False

    @pytest.mark.asyncio
    async def test_send_notification_exception_handled(self, monkeypatch):
        """测试 _send_notification 中 apprise.notify 抛异常时不向上传播"""

        def mock_notify_raises(message):
            raise RuntimeError("network error")

        mock_apprise_module = MagicMock()
        mock_apprise_instance = MagicMock()
        mock_apprise_instance.notify = mock_notify_raises
        mock_apprise_module.Apprise.return_value = mock_apprise_instance
        monkeypatch.setitem(sys.modules, "apprise", mock_apprise_module)

        config = {
            "notifier": {
                "enabled": True,
                "service_urls": ["tgram://token/chat_id"],
            }
        }
        notifier = Notifier(config)

        # 不应抛出异常，内部 try/except 会捕获并记录日志
        await notifier.notify("afdian", 403, "Forbidden")
