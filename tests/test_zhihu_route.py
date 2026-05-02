"""测试知乎路由"""

import pytest
from datetime import datetime, timezone
from unittest.mock import AsyncMock, patch, MagicMock

from RSSGen.routes.zhihu import ZhihuRoute, TYPE_ANSWER, TYPE_ARTICLE, TYPE_PIN
from RSSGen.sign.zhihu.sign import X_ZSE_93_VERSION, X_ZSE_96_PREFIX, get_signature


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

    def test_pin_falls_back_to_excerpt_title(self, route):
        """excerpt 为 None 时回退到 excerpt_title"""
        target = {
            "id": "222",
            "type": TYPE_PIN,
            "excerpt": None,
            "excerpt_title": "5.2早安<br>大家好",
            "created": 1700000000,
            "author": {"name": "作者"},
        }
        item = route._make_feed_item(target)
        assert "5.2早安" in item.title
        assert "<br>" not in item.title

    def test_answer_type_sets_categories(self, route):
        target = {
            "id": "123",
            "type": TYPE_ANSWER,
            "content": "<p>内容</p>",
            "created_time": 1700000000,
            "author": {"name": "作者"},
            "question": {"id": "456", "title": "问题标题"},
        }
        item = route._make_feed_item(target)
        assert item.categories == [TYPE_ANSWER]

    def test_article_type_sets_categories(self, route):
        target = {
            "id": "789",
            "type": TYPE_ARTICLE,
            "title": "文章标题",
            "content": "<p>内容</p>",
            "created_time": 1700000000,
            "author": {"name": "作者"},
        }
        item = route._make_feed_item(target)
        assert item.categories == [TYPE_ARTICLE]

    def test_pin_type_sets_categories(self, route):
        target = {
            "id": "111",
            "type": TYPE_PIN,
            "excerpt": "想法摘要",
            "created_time": 1700000000,
            "author": {"name": "作者"},
        }
        item = route._make_feed_item(target)
        assert item.categories == [TYPE_PIN]

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

    def test_pin_uses_created_field_for_pub_date(self, route):
        """pin 类型用 created 字段而非 created_time"""
        target = {
            "id": "111",
            "type": TYPE_PIN,
            "excerpt": "想法",
            "created_time": None,
            "created": 1700000000,
            "author": {"name": "a"},
        }
        item = route._make_feed_item(target)
        assert item.pub_date == datetime(2023, 11, 14, 22, 13, 20, tzinfo=timezone.utc)


class TestZhihuRouteFetch:
    @pytest.mark.asyncio
    async def test_fetch_returns_feed_items(self):
        route = ZhihuRoute({"cookie": "d_c0=test; other=val"})

        activities = [
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

        with patch.object(
            route,
            "_fetch_activities",
            new_callable=AsyncMock,
            return_value=activities,
        ):
            items = await route.fetch(path_params=["kvxjr369f"])

        assert len(items) == 2
        assert items[0].title == "问题标题"
        assert items[1].title == "文章标题"


class TestZhihuRouteFetchFilter:
    @pytest.mark.asyncio
    async def test_fetch_filters_by_include(self):
        """配置 include=[answer] 时只返回 answer 类型"""
        route = ZhihuRoute({
            "cookie": "d_c0=test",
            "feeds": [{"user_id": "test_user", "include": ["answer"]}],
        })

        activities = [
            {
                "id": "act1",
                "type": "feed",
                "target": {
                    "id": "1",
                    "type": TYPE_ANSWER,
                    "content": "<p>回答</p>",
                    "created_time": 1700000000,
                    "author": {"name": "A"},
                    "question": {"id": "10", "title": "问题"},
                },
            },
            {
                "id": "act2",
                "type": "feed",
                "target": {
                    "id": "2",
                    "type": TYPE_PIN,
                    "excerpt": "想法内容",
                    "created_time": 1700000100,
                    "author": {"name": "A"},
                },
            },
        ]

        with patch.object(
            route, "_fetch_activities", new_callable=AsyncMock, return_value=activities
        ):
            items = await route.fetch(path_params=["test_user"])

        assert len(items) == 1
        assert items[0].categories == [TYPE_ANSWER]

    @pytest.mark.asyncio
    async def test_fetch_returns_all_when_no_include(self):
        """不配置 include 时返回全部类型"""
        route = ZhihuRoute({"cookie": "d_c0=test"})

        activities = [
            {
                "id": "act1",
                "type": "feed",
                "target": {
                    "id": "1",
                    "type": TYPE_ANSWER,
                    "content": "<p>回答</p>",
                    "created_time": 1700000000,
                    "author": {"name": "A"},
                    "question": {"id": "10", "title": "问题"},
                },
            },
            {
                "id": "act2",
                "type": "feed",
                "target": {
                    "id": "2",
                    "type": TYPE_PIN,
                    "excerpt": "想法",
                    "created_time": 1700000100,
                    "author": {"name": "A"},
                },
            },
        ]

        with patch.object(
            route, "_fetch_activities", new_callable=AsyncMock, return_value=activities
        ):
            items = await route.fetch(path_params=["test_user"])

        assert len(items) == 2

    @pytest.mark.asyncio
    async def test_fetch_include_from_kwargs(self):
        """refresher 通过 kwargs 透传 include"""
        route = ZhihuRoute({"cookie": "d_c0=test"})

        activities = [
            {
                "id": "act1",
                "type": "feed",
                "target": {
                    "id": "1",
                    "type": TYPE_ANSWER,
                    "content": "<p>回答</p>",
                    "created_time": 1700000000,
                    "author": {"name": "A"},
                    "question": {"id": "10", "title": "问题"},
                },
            },
            {
                "id": "act2",
                "type": "feed",
                "target": {
                    "id": "2",
                    "type": TYPE_ARTICLE,
                    "title": "文章",
                    "content": "<p>内容</p>",
                    "created_time": 1700000100,
                    "author": {"name": "A"},
                },
            },
        ]

        with patch.object(
            route, "_fetch_activities", new_callable=AsyncMock, return_value=activities
        ):
            items = await route.fetch(
                path_params=["test_user"], include=[TYPE_PIN]
            )

        assert len(items) == 0  # kwargs 中 include=[pin]，但数据中没有 pin


class TestZhihuRouteFetchWithSigner:
    @pytest.mark.asyncio
    async def test_fetch_with_real_signature_calls_api(self):
        pytest.skip("integration test - 需要真实 Cookie")

    @pytest.mark.asyncio
    async def test_fetch_builds_correct_headers(self, route_with_dc0):
        url = "https://www.zhihu.com/api/v3/moments/test_user/activities"
        d_c0 = route_with_dc0._get_d_c0()

        sig = get_signature(url, d_c0)

        assert sig["x_zse_93"] == X_ZSE_93_VERSION
        assert sig["x_zse_96"].startswith(X_ZSE_96_PREFIX)


def _patch_async_session(monkeypatch, responses):
    """把 RSSGen.routes.zhihu.AsyncSession 替换为按 responses 顺序返回的 mock。

    responses: list of MagicMock，每个元素是单次 session.get 的返回值。
    返回创建的 session mock 以便断言 get 调用。
    """
    session = AsyncMock()
    session.get = AsyncMock(side_effect=responses)
    cm = MagicMock()
    cm.__aenter__ = AsyncMock(return_value=session)
    cm.__aexit__ = AsyncMock(return_value=None)
    monkeypatch.setattr(
        "RSSGen.routes.zhihu.AsyncSession", MagicMock(return_value=cm)
    )
    return session


def _make_page(data: list[dict], next_url: str | None, is_end: bool = False):
    resp = MagicMock()
    resp.status_code = 200
    resp.json.return_value = {
        "data": data,
        "paging": {"is_end": is_end, "next": next_url},
    }
    return resp


class TestZhihuRouteFetchActivitiesPagination:
    @pytest.mark.asyncio
    async def test_stops_when_limit_reached(self, monkeypatch, route_with_dc0):
        """累计达到 limit 后停止翻页并截断"""
        page1 = _make_page(
            [{"id": str(i)} for i in range(8)],
            next_url="https://www.zhihu.com/api/v3/moments/u/activities?offset=A&page_num=1",
        )
        page2 = _make_page(
            [{"id": str(i)} for i in range(8, 15)],
            next_url="https://www.zhihu.com/api/v3/moments/u/activities?offset=B&page_num=2",
        )
        session = _patch_async_session(monkeypatch, [page1, page2])

        result = await route_with_dc0._fetch_activities("u", limit=10)

        assert len(result) == 10
        assert session.get.call_count == 2

    @pytest.mark.asyncio
    async def test_stops_when_is_end_true(self, monkeypatch, route_with_dc0):
        """is_end=True 时即使未达到 limit 也停止"""
        page1 = _make_page(
            [{"id": str(i)} for i in range(3)],
            next_url=None,
            is_end=True,
        )
        session = _patch_async_session(monkeypatch, [page1])

        result = await route_with_dc0._fetch_activities("u", limit=20)

        assert len(result) == 3
        assert session.get.call_count == 1

    @pytest.mark.asyncio
    async def test_uses_paging_next_url(self, monkeypatch, route_with_dc0):
        """第二次请求使用 paging.next 中的完整 URL"""
        next_url = (
            "https://www.zhihu.com/api/v3/moments/u/activities?offset=1777652939991&page_num=1"
        )
        page1 = _make_page([{"id": "a"}], next_url=next_url)
        page2 = _make_page([{"id": "b"}], next_url=None, is_end=True)
        session = _patch_async_session(monkeypatch, [page1, page2])

        result = await route_with_dc0._fetch_activities("u", limit=20)

        assert len(result) == 2
        # 第一次用首页 URL（带 limit=5），第二次必须是 paging.next 提供的 URL
        first_call_url = session.get.call_args_list[0].args[0]
        second_call_url = session.get.call_args_list[1].args[0]
        assert "limit=5" in first_call_url
        assert second_call_url == next_url

    @pytest.mark.asyncio
    async def test_stops_when_next_is_missing(self, monkeypatch, route_with_dc0):
        """paging.next 为空时停止（即使 is_end 不是 True）"""
        page1 = _make_page([{"id": "a"}], next_url=None, is_end=False)
        session = _patch_async_session(monkeypatch, [page1])

        result = await route_with_dc0._fetch_activities("u", limit=20)

        assert len(result) == 1
        assert session.get.call_count == 1

    @pytest.mark.asyncio
    async def test_raises_on_non_200(self, monkeypatch, route_with_dc0):
        """非 200 状态码抛 RuntimeError"""
        bad = MagicMock(status_code=403)
        _patch_async_session(monkeypatch, [bad])

        with pytest.raises(RuntimeError, match="403"):
            await route_with_dc0._fetch_activities("u", limit=20)
