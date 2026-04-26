"""测试知乎路由"""

import pytest
from datetime import datetime, timezone

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


class TestZhihuRouteMakeFeedItem:
    def test_answer_type_extracts_question_title(self):
        """回答类型从 question.title 提取标题"""
        route = ZhihuRoute({"cookie": "test"})
        target = {
            "id": "123",
            "type": "answer",
            "content": "<p>回答内容</p>",
            "created_time": 1700000000,
            "author": {"name": "作者"},
            "question": {"id": "456", "title": "问题标题"},
        }

        item = route._make_feed_item(target)

        assert item.title == "问题标题"
        assert item.link == "https://www.zhihu.com/question/456/answer/123"
        assert item.guid == "123"
        assert item.author == "作者"

    def test_article_type_extracts_target_title(self):
        """文章类型从 target.title 提取标题"""
        route = ZhihuRoute({"cookie": "test"})
        target = {
            "id": "789",
            "type": "article",
            "title": "文章标题",
            "content": "<p>文章内容</p>",
            "created_time": 1700000000,
            "author": {"name": "作者"},
        }

        item = route._make_feed_item(target)

        assert item.title == "文章标题"
        assert item.link == "https://zhuanlan.zhihu.com/p/789"

    def test_pin_type_uses_excerpt_as_title(self):
        """想法类型使用摘要作为标题"""
        route = ZhihuRoute({"cookie": "test"})
        target = {
            "id": "111",
            "type": "pin",
            "excerpt": "这是一条想法的摘要内容",
            "created_time": 1700000000,
            "author": {"name": "作者"},
        }

        item = route._make_feed_item(target)

        assert "摘要内容" in item.title
        assert item.link == "https://www.zhihu.com/pin/111"

    def test_pub_date_from_created_time(self):
        """pub_date 从 created_time Unix 时间戳转换"""
        route = ZhihuRoute({"cookie": "test"})
        target = {
            "id": "123",
            "type": "answer",
            "created_time": 1700000000,
            "content": "",
            "author": {"name": "a"},
            "question": {"id": "1", "title": "t"},
        }

        item = route._make_feed_item(target)

        assert item.pub_date == datetime(2023, 11, 14, 22, 13, 20, tzinfo=timezone.utc)