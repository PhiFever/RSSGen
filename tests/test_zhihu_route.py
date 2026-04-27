"""测试知乎路由"""

import pytest
from datetime import datetime, timezone
from unittest.mock import AsyncMock, patch, MagicMock

from RSSGen.routes.zhihu import ZhihuRoute, ZhihuSigner, TYPE_ANSWER, TYPE_ARTICLE, TYPE_PIN, X_ZSE_93_VERSION, X_ZSE_96_PREFIX


@pytest.fixture
def route():
    return ZhihuRoute({"cookie": "test"})


@pytest.fixture
def route_with_dc0():
    return ZhihuRoute({"cookie": "d_c0=test_value"})


class TestZhihuRouteFeedInfo:
    @pytest.mark.asyncio
    async def test_feed_info_returns_correct_title_and_link(self, route):
        info = await route.feed_info(path_params=["kvxjr369f"])

        assert info.title == "知乎动态 - kvxjr369f"
        assert info.link == "https://www.zhihu.com/people/kvxjr369f"
        assert "kvxjr369f" in info.description

    @pytest.mark.asyncio
    async def test_feed_info_requires_user_id(self, route):
        with pytest.raises(ValueError, match="需要指定用户"):
            await route.feed_info()


class TestZhihuRouteMakeFeedItem:
    def test_answer_type_extracts_question_title(self, route):
        target = {
            "id": "123",
            "type": TYPE_ANSWER,
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

    def test_article_type_extracts_target_title(self, route):
        target = {
            "id": "789",
            "type": TYPE_ARTICLE,
            "title": "文章标题",
            "content": "<p>文章内容</p>",
            "created_time": 1700000000,
            "author": {"name": "作者"},
        }

        item = route._make_feed_item(target)

        assert item.title == "文章标题"
        assert item.link == "https://zhuanlan.zhihu.com/p/789"

    def test_pin_type_uses_excerpt_as_title(self, route):
        target = {
            "id": "111",
            "type": TYPE_PIN,
            "excerpt": "这是一条想法的摘要内容",
            "created_time": 1700000000,
            "author": {"name": "作者"},
        }

        item = route._make_feed_item(target)

        assert "摘要内容" in item.title
        assert item.link == "https://www.zhihu.com/pin/111"

    def test_pub_date_from_created_time(self, route):
        target = {
            "id": "123",
            "type": TYPE_ANSWER,
            "created_time": 1700000000,
            "content": "",
            "author": {"name": "a"},
            "question": {"id": "1", "title": "t"},
        }

        item = route._make_feed_item(target)

        assert item.pub_date == datetime(2023, 11, 14, 22, 13, 20, tzinfo=timezone.utc)


class TestZhihuRouteFetch:
    @pytest.mark.asyncio
    async def test_fetch_returns_feed_items(self):
        route = ZhihuRoute({"cookie": "d_c0=test; other=val"})

        mock_response = MagicMock()
        mock_response.status_code = 200
        mock_response.json.return_value = {
            "data": [
                {
                    "id": "act1",
                    "type": "feed",
                    "target": {
                        "id": "123",
                        "type": TYPE_ANSWER,
                        "content": "<p>内容</p>",
                        "created_time": 1700000000,
                        "author": {"name": "作者"},
                        "question": {"id": "456", "title": "问题标题"},
                    },
                },
                {
                    "id": "act2",
                    "type": "feed",
                    "target": {
                        "id": "789",
                        "type": TYPE_ARTICLE,
                        "title": "文章标题",
                        "content": "<p>文章内容</p>",
                        "created_time": 1700000100,
                        "author": {"name": "作者2"},
                    },
                },
            ]
        }

        with (
            patch.object(route, "_fetch_activities", new_callable=AsyncMock, return_value=mock_response),
        ):
            items = await route.fetch(path_params=["kvxjr369f"])

        assert len(items) == 2
        assert items[0].title == "问题标题"
        assert items[1].title == "文章标题"


class TestZhihuRouteFetchWithSigner:
    @pytest.mark.asyncio
    async def test_fetch_with_real_signature_calls_api(self):
        pytest.skip("integration test - 需要真实 Cookie")

    @pytest.mark.asyncio
    async def test_fetch_builds_correct_headers(self, route_with_dc0):
        url = "https://www.zhihu.com/api/v3/moments/test_user/activities"
        d_c0 = route_with_dc0._get_d_c0()

        signer = ZhihuSigner()
        sig = signer.get_signature(url, d_c0)

        assert sig["x_zse_93"] == X_ZSE_93_VERSION
        assert sig["x_zse_96"].startswith(X_ZSE_96_PREFIX)