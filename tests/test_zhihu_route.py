"""测试知乎路由"""

import pytest
from datetime import datetime, timezone
from unittest.mock import AsyncMock, patch, MagicMock

from RSSGen.routes.zhihu import (
    ZhihuRoute,
    TYPE_ANSWER,
    TYPE_ARTICLE,
    TYPE_PIN,
    TYPE_COLLECTED_ANSWER,
    TYPE_COLLECTED_ARTICLE,
    TYPE_VOTEUP_ANSWER,
    TYPE_FOLLOWED_QUESTION,
)
from RSSGen.sign.zhihu.sign import X_ZSE_93_VERSION, X_ZSE_96_PREFIX, get_signature


def _act(target, verb="", action_text=""):
    """把 target 包装成 activity dict，便于复用旧 fixture 数据"""
    return {"target": target, "verb": verb, "action_text": action_text}


@pytest.fixture
def route():
    return ZhihuRoute({"cookie": "test"})


@pytest.fixture
def route_with_dc0():
    return ZhihuRoute({"cookie": "d_c0=test_value"})


class TestZhihuFixLazyImages:
    def test_fixes_svg_placeholder_with_data_actualsrc(self, route):
        html = '<img src="data:image/svg+xml;utf8,<svg xmlns=\'http://www.w3.org/2000/svg\' width=\'100\' height=\'50\'></svg>" data-actualsrc="https://pic.zhimg.com/test_b.jpg" class="lazy">'
        result = route._fix_lazy_images(html)
        assert "https://pic.zhimg.com/test_b.jpg" in result
        assert "data:image/svg+xml" not in result
        assert "lazy" not in result

    def test_fixes_svg_placeholder_with_data_original(self, route):
        html = '<img src="data:image/svg+xml;utf8,<svg xmlns=\'http://www.w3.org/2000/svg\' width=\'100\' height=\'50\'></svg>" data-original="https://pic.zhimg.com/test_r.jpg">'
        result = route._fix_lazy_images(html)
        assert "https://pic.zhimg.com/test_r.jpg" in result
        assert "data:image/svg+xml" not in result

    def test_prioritizes_data_actualsrc_over_data_original(self, route):
        html = '<img src="data:image/svg+xml" data-actualsrc="https://pic.zhimg.com/actual.jpg" data-original="https://pic.zhimg.com/original.jpg">'
        result = route._fix_lazy_images(html)
        assert "actual.jpg" in result
        assert "original.jpg" not in result

    def test_removes_noscript_tags(self, route):
        html = '<figure><noscript><img src="https://pic.zhimg.com/real.jpg"></noscript><img src="data:image/svg+xml" data-actualsrc="https://pic.zhimg.com/real.jpg"></figure>'
        result = route._fix_lazy_images(html)
        assert "<noscript>" not in result
        assert "https://pic.zhimg.com/real.jpg" in result

    def test_preserves_normal_images(self, route):
        html = '<img src="https://pic.zhimg.com/normal.jpg">'
        result = route._fix_lazy_images(html)
        assert "https://pic.zhimg.com/normal.jpg" in result

    def test_handles_empty_html(self, route):
        assert route._fix_lazy_images("") == ""
        assert route._fix_lazy_images(None) is None


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
            "type": "answer",
            "content": "<p>回答内容</p>",
            "created_time": 1700000000,
            "author": {"name": "作者"},
            "question": {"id": "456", "title": "问题标题"},
        }

        item = route._make_feed_item(
            _act(target, "MEMBER_ANSWER_QUESTION", "回答了问题")
        )

        assert item.title == "[回答了问题] 问题标题"
        assert item.link == "https://www.zhihu.com/question/456/answer/123"
        assert item.guid == "123"
        assert item.author == "作者"

    def test_article_type_extracts_target_title(self, route):
        target = {
            "id": "789",
            "type": "article",
            "title": "文章标题",
            "content": "<p>文章内容</p>",
            "created_time": 1700000000,
            "author": {"name": "作者"},
        }

        item = route._make_feed_item(
            _act(target, "MEMBER_CREATE_ARTICLE", "发表了文章")
        )

        assert item.title == "[发表了文章] 文章标题"
        assert item.link == "https://zhuanlan.zhihu.com/p/789"

    def test_pin_type_uses_excerpt_as_title(self, route):
        target = {
            "id": "111",
            "type": "pin",
            "excerpt": "这是一条想法的摘要内容",
            "created_time": 1700000000,
            "author": {"name": "作者"},
        }

        item = route._make_feed_item(_act(target, "MEMBER_CREATE_PIN", "发布了想法"))

        assert item.title == "[发布了想法] 这是一条想法的摘要内容"

    def test_pin_falls_back_to_excerpt_title(self, route):
        """excerpt 为 None 时回退到 excerpt_title"""
        target = {
            "id": "222",
            "type": "pin",
            "excerpt": None,
            "excerpt_title": "5.2早安<br>大家好",
            "created": 1700000000,
            "author": {"name": "作者"},
        }
        item = route._make_feed_item(_act(target, "MEMBER_CREATE_PIN", "发布了想法"))
        assert "5.2早安" in item.title
        assert "<br>" not in item.title
        assert item.title.startswith("[发布了想法] ")

    def test_answer_verb_sets_answer_category(self, route):
        target = {
            "id": "123",
            "type": "answer",
            "content": "<p>内容</p>",
            "created_time": 1700000000,
            "author": {"name": "作者"},
            "question": {"id": "456", "title": "问题标题"},
        }
        item = route._make_feed_item(
            _act(target, "MEMBER_ANSWER_QUESTION", "回答了问题")
        )
        assert item.categories == [TYPE_ANSWER]
        assert item.guid == "123"
        assert item.title == "[回答了问题] 问题标题"

    def test_collect_verb_sets_collected_answer_category(self, route):
        """收藏回答 → category=collected_answer，title 加动作前缀，guid 加 category 前缀"""
        target = {
            "id": "123",
            "type": "answer",
            "content": "<p>内容</p>",
            "created_time": 1600000000,
            "author": {"name": "原作者"},
            "question": {"id": "456", "title": "问题标题"},
        }
        item = route._make_feed_item(
            {
                "target": target,
                "verb": "MEMBER_COLLECT_ANSWER",
                "action_text": "收藏了回答",
                "created_time": 1700000000,
            }
        )
        assert item.categories == [TYPE_COLLECTED_ANSWER]
        assert item.title == "[收藏了回答] 问题标题"
        assert item.guid == "collected_answer_123"
        # pub_date 用 act.created_time（收藏时刻），不是 target.created_time
        assert item.pub_date == datetime(2023, 11, 14, 22, 13, 20, tzinfo=timezone.utc)

    def test_collect_article_verb(self, route):
        target = {
            "id": "789",
            "type": "article",
            "title": "文章标题",
            "content": "<p>内容</p>",
            "created_time": 1700000000,
            "author": {"name": "作者"},
        }
        item = route._make_feed_item(
            _act(target, "MEMBER_COLLECT_ARTICLE", "收藏了文章")
        )
        assert item.categories == [TYPE_COLLECTED_ARTICLE]
        assert item.title == "[收藏了文章] 文章标题"

    def test_voteup_answer_verb(self, route):
        """点赞回答 → category=voteup_answer"""
        target = {
            "id": "888",
            "type": "answer",
            "content": "<p>内容</p>",
            "created_time": 1700000000,
            "author": {"name": "作者"},
            "question": {"id": "999", "title": "被赞同的问题"},
        }
        item = route._make_feed_item(_act(target, "MEMBER_VOTEUP_ANSWER", "赞同了回答"))
        assert item.categories == [TYPE_VOTEUP_ANSWER]
        assert item.title == "[赞同了回答] 被赞同的问题"
        assert item.guid == "voteup_answer_888"

    def test_followed_question_via_empty_verb(self, route):
        """关注问题 verb 为空、target.type=question → category=followed_question"""
        target = {
            "id": "2033612101923464726",
            "type": "question",
            "title": "张雪回应 820 赛道熄火",
            "detail": "<p>问题描述</p>",
            "excerpt": "问题摘要",
            "created": 1777630907,
            "author": {"name": "提问者"},
        }
        item = route._make_feed_item(
            {
                "target": target,
                "verb": "",
                "action_text": "关注了问题",
                "created_time": 1777708519,
            }
        )
        assert item.categories == [TYPE_FOLLOWED_QUESTION]
        assert item.title == "[关注了问题] 张雪回应 820 赛道熄火"
        assert item.link == "https://www.zhihu.com/question/2033612101923464726"
        assert item.guid == "followed_question_2033612101923464726"
        # content 在没有 content 字段时回退到 detail
        assert item.content == "<p>问题描述</p>"

    def test_pub_date_from_target_created_time(self, route):
        """没有 act.created_time 时回退到 target.created_time"""
        target = {
            "id": "123",
            "type": "answer",
            "created_time": 1700000000,
            "content": "",
            "author": {"name": "a"},
            "question": {"id": "1", "title": "t"},
        }

        item = route._make_feed_item(_act(target, "MEMBER_ANSWER_QUESTION"))

        assert item.pub_date == datetime(2023, 11, 14, 22, 13, 20, tzinfo=timezone.utc)

    def test_pin_uses_created_field_for_pub_date(self, route):
        """pin 类型用 created 字段而非 created_time"""
        target = {
            "id": "111",
            "type": "pin",
            "excerpt": "想法",
            "created_time": None,
            "created": 1700000000,
            "author": {"name": "a"},
        }
        item = route._make_feed_item(_act(target, "MEMBER_CREATE_PIN"))
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
        route = ZhihuRoute(
            {
                "cookie": "d_c0=test",
                "feeds": [{"user_id": "test_user", "include": ["answer"]}],
            }
        )

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
    async def test_fetch_include_excludes_collected_when_only_answer(self):
        """include=[answer] 时，target.type=answer 但 verb=COLLECT 的活动被排除"""
        route = ZhihuRoute(
            {
                "cookie": "d_c0=test",
                "feeds": [{"user_id": "test_user", "include": ["answer"]}],
            }
        )

        activities = [
            {  # 自己回答 → category=answer，命中 include
                "id": "act1",
                "verb": "MEMBER_ANSWER_QUESTION",
                "action_text": "回答了问题",
                "target": {
                    "id": "1",
                    "type": "answer",
                    "content": "<p>自己写的回答</p>",
                    "created_time": 1700000000,
                    "author": {"name": "用户"},
                    "question": {"id": "10", "title": "问题A"},
                },
            },
            {  # 收藏的回答 → category=collected_answer，被排除
                "id": "act2",
                "verb": "MEMBER_COLLECT_ANSWER",
                "action_text": "收藏了回答",
                "target": {
                    "id": "2",
                    "type": "answer",
                    "content": "<p>别人的回答</p>",
                    "created_time": 1700000100,
                    "author": {"name": "他人"},
                    "question": {"id": "20", "title": "问题B"},
                },
            },
        ]

        with patch.object(
            route, "_fetch_activities", new_callable=AsyncMock, return_value=activities
        ):
            items = await route.fetch(path_params=["test_user"])

        assert len(items) == 1
        assert items[0].title == "[回答了问题] 问题A"
        assert items[0].categories == [TYPE_ANSWER]

    @pytest.mark.asyncio
    async def test_fetch_include_collected_answer(self):
        """include=[collected_answer] 时只返回收藏的回答"""
        route = ZhihuRoute(
            {
                "cookie": "d_c0=test",
                "feeds": [{"user_id": "test_user", "include": ["collected_answer"]}],
            }
        )

        activities = [
            {
                "id": "act1",
                "verb": "MEMBER_ANSWER_QUESTION",
                "action_text": "回答了问题",
                "target": {
                    "id": "1",
                    "type": "answer",
                    "content": "",
                    "created_time": 1700000000,
                    "author": {"name": "u"},
                    "question": {"id": "10", "title": "Q1"},
                },
            },
            {
                "id": "act2",
                "verb": "MEMBER_COLLECT_ANSWER",
                "action_text": "收藏了回答",
                "target": {
                    "id": "2",
                    "type": "answer",
                    "content": "",
                    "created_time": 1700000100,
                    "author": {"name": "u2"},
                    "question": {"id": "20", "title": "Q2"},
                },
            },
        ]

        with patch.object(
            route, "_fetch_activities", new_callable=AsyncMock, return_value=activities
        ):
            items = await route.fetch(path_params=["test_user"])

        assert len(items) == 1
        assert items[0].categories == [TYPE_COLLECTED_ANSWER]
        assert items[0].title.startswith("[收藏了回答]")

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
            items = await route.fetch(path_params=["test_user"], include=[TYPE_PIN])

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
    monkeypatch.setattr("RSSGen.routes.zhihu.AsyncSession", MagicMock(return_value=cm))
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
        next_url = "https://www.zhihu.com/api/v3/moments/u/activities?offset=1777652939991&page_num=1"
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
