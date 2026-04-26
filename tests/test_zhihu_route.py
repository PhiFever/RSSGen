"""测试知乎路由"""

import pytest

from RSSGen.routes.zhihu import ZhihuRoute


class TestZhihuRouteFeedInfo:
    @pytest.mark.asyncio
    async def test_feed_info_returns_correct_title_and_link(self):
        """feed_info 返回正确的标题和链接"""
        route = ZhihuRoute({"cookie": "test"})
        info = await route.feed_info(path_params=["kvxjr369f"])

        assert info.title == "知乎动态 - kvxjr369f"
        assert info.link == "https://www.zhihu.com/people/kvxjr369f"
        assert "kvxjr369f" in info.description

    @pytest.mark.asyncio
    async def test_feed_info_requires_user_id(self):
        """缺少 user_id 时抛出异常"""
        route = ZhihuRoute({"cookie": "test"})

        with pytest.raises(ValueError, match="需要指定用户"):
            await route.feed_info()