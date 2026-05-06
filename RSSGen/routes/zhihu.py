"""知乎用户动态路由"""

import html
import re
from datetime import datetime, timezone

from bs4 import BeautifulSoup
from curl_cffi.requests import AsyncSession
from loguru import logger

from RSSGen.core.route import FeedInfo, FeedItem, Route
from RSSGen.core.utils import lookup_alias, parse_cookie_string
from RSSGen.sign.zhihu.sign import get_signature

# 动态 category 常量（按 verb + target.type 派生，比 target.type 更细分）
TYPE_ANSWER = "answer"  # 自己回答问题
TYPE_ARTICLE = "article"  # 自己创作文章
TYPE_PIN = "pin"  # 自己发布想法
TYPE_COLLECTED_ANSWER = "collected_answer"
TYPE_COLLECTED_ARTICLE = "collected_article"
TYPE_COLLECTED_PIN = "collected_pin"
TYPE_VOTEUP_ANSWER = "voteup_answer"
TYPE_VOTEUP_ARTICLE = "voteup_article"
TYPE_FOLLOWED_QUESTION = "followed_question"

# verb 直接映射 category
_VERB_CATEGORY_MAP = {
    "MEMBER_ANSWER_QUESTION": TYPE_ANSWER,
    "MEMBER_CREATE_ARTICLE": TYPE_ARTICLE,
    "MEMBER_CREATE_PIN": TYPE_PIN,
    "MEMBER_COLLECT_ANSWER": TYPE_COLLECTED_ANSWER,
    "MEMBER_COLLECT_ARTICLE": TYPE_COLLECTED_ARTICLE,
    "MEMBER_COLLECT_PIN": TYPE_COLLECTED_PIN,
    "MEMBER_VOTEUP_ANSWER": TYPE_VOTEUP_ANSWER,
    "MEMBER_VOTEUP_ARTICLE": TYPE_VOTEUP_ARTICLE,
    "MEMBER_FOLLOW_QUESTION": TYPE_FOLLOWED_QUESTION,
}

# verb 缺失或未知时按 target.type 兜底；关注问题时 verb 为空、target.type=question
_TARGET_TYPE_FALLBACK = {
    "answer": TYPE_ANSWER,
    "article": TYPE_ARTICLE,
    "pin": TYPE_PIN,
    "question": TYPE_FOLLOWED_QUESTION,
}


def _derive_category(act: dict) -> str:
    """从 activity 派生 category：先看 verb，再用 target.type 兜底"""
    verb = act.get("verb", "")
    if verb in _VERB_CATEGORY_MAP:
        return _VERB_CATEGORY_MAP[verb]
    target_type = act.get("target", {}).get("type", "unknown")
    return _TARGET_TYPE_FALLBACK.get(target_type, target_type)


class ZhihuRoute(Route):
    """知乎用户动态路由"""

    name = "zhihu"
    description = "知乎用户动态订阅"

    @staticmethod
    def _fix_lazy_images(html: str) -> str:
        """修复知乎懒加载图片，将 SVG 占位符替换为真实 URL"""
        if not html:
            return html

        soup = BeautifulSoup(html, "html.parser")

        for img in soup.find_all("img"):
            src = img.get("src", "")
            # 检测 SVG 占位符
            if src.startswith("data:image/svg+xml"):
                # 优先使用 data-actualsrc，其次是 data-original
                actual_src = img.get("data-actualsrc") or img.get("data-original")
                if actual_src:
                    img["src"] = actual_src
                    # 移除懒加载相关属性
                    img.attrs.pop("data-actualsrc", None)
                    img.attrs.pop("data-original", None)
                    if "lazy" in img.get("class", []):
                        img["class"].remove("lazy")

        # 移除 <noscript> 标签（其内容已包含真实图片，但会被 RSS 阅读器忽略）
        for noscript in soup.find_all("noscript"):
            noscript.decompose()

        return str(soup)

    async def feed_info(self, **kwargs) -> FeedInfo:
        path_params: list[str] = kwargs.get("path_params", [])
        if not path_params:
            raise ValueError("需要指定用户 ID，如 /feed/zhihu/{user_id}")

        user_id = path_params[0]
        display_name = (
            lookup_alias(self.config.get("feeds"), "user_id", user_id) or user_id
        )
        return FeedInfo(
            title=f"知乎动态 - {display_name}",
            link=f"https://www.zhihu.com/people/{user_id}",
            description=f"知乎用户 {display_name} 的最新动态",
        )

    @staticmethod
    def _render_pin_content(blocks: list) -> str:
        """将 PIN 类型的 content blocks (list of dict) 渲染为 HTML"""
        parts = []
        for block in blocks:
            if not isinstance(block, dict):
                continue
            text = block.get("content")
            if text:
                parts.append(text)
                continue
            block_type = block.get("type", "")
            if block_type == "image":
                url = block.get("original_url") or block.get("url", "")
                if url:
                    parts.append(f'<img src="{html.escape(url, quote=True)}" />')
            elif block_type == "link_card":
                url = block.get("url", "")
                title = block.get("data_draft_title") or url
                if url:
                    parts.append(
                        f'<a href="{html.escape(url, quote=True)}">{html.escape(title)}</a>'
                    )
        return "<br/>".join(parts)

    def _make_feed_item(self, act: dict) -> FeedItem:
        """根据 activity dict 构造 FeedItem"""
        target = act.get("target", {})
        target_id = target.get("id", "")
        target_type = target.get("type", "unknown")
        category = _derive_category(act)

        # 按 target.type（数据结构）选择字段，与 category（动作语义）解耦
        if target_type == "answer":
            question = target.get("question", {})
            title = question.get("title", "")
            question_id = question.get("id", "")
            link = f"https://www.zhihu.com/question/{question_id}/answer/{target_id}"
        elif target_type == "article":
            title = target.get("title", "")
            link = f"https://zhuanlan.zhihu.com/p/{target_id}"
        elif target_type == "pin":
            excerpt = target.get("excerpt_title") or target.get("excerpt") or ""
            title = re.sub(r"<[^>]+>", "", excerpt)[:50] if excerpt else "想法"
            link = f"https://www.zhihu.com/pin/{target_id}"
        elif target_type == "question":
            title = target.get("title", "")
            link = f"https://www.zhihu.com/question/{target_id}"
        else:
            title = target.get("title", target.get("excerpt", "未知内容")[:50])
            link = f"https://www.zhihu.com/{target_type}/{target_id}"

        # 所有活动都加 action 前缀，方便在 RSS 阅读器中一眼辨别动作类型
        action_text = act.get("action_text", "")
        if action_text:
            title = f"[{action_text}] {title}"

        # guid：自己创作沿用 target_id（兼容旧订阅去重）；其他 case 加 category 前缀
        guid = (
            target_id
            if category in (TYPE_ANSWER, TYPE_ARTICLE, TYPE_PIN)
            else f"{category}_{target_id}"
        )

        # pub_date 优先用 act.created_time（动作发生时间），否则回退到 target 时间
        created_time = (
            act.get("created_time")
            or target.get("created_time")
            or target.get("created", 0)
        )
        pub_date = (
            datetime.fromtimestamp(created_time, tz=timezone.utc)
            if created_time
            else None
        )

        raw_content = target.get("content")
        if isinstance(raw_content, list):
            content = self._render_pin_content(raw_content)
        else:
            content = raw_content or target.get("detail") or target.get("excerpt", "")
        # 修复懒加载图片占位符
        content = self._fix_lazy_images(content)
        author = target.get("author", {}).get("name", "")

        return FeedItem(
            title=title,
            link=link,
            content=content,
            pub_date=pub_date,
            author=author,
            guid=guid,
            categories=[category],
        )

    def _get_feed_include(self, user_id: str) -> list[str] | None:
        """从 self.config 的 feeds 列表中查找指定用户的 include 配置"""
        for feed in self.config.get("feeds", []):
            if feed.get("user_id") == user_id:
                return feed.get("include")
        return None

    def _get_d_c0(self) -> str:
        """从 cookie 提取 d_c0"""
        cookie_str = self.config.get("cookie", "")
        match = re.search(r"d_c0=([^;]+)", cookie_str)
        if match:
            return match.group(1)
        raise ValueError("Cookie 中缺少 d_c0 字段")

    async def _fetch_activities(self, user_id: str, limit: int) -> list[dict]:
        """请求知乎用户动态 API，按 paging.next 翻页累积，直到达到 limit 或 is_end"""
        # 第一页固定 limit=5，与浏览器观察到的请求一致；后续页 URL 由 paging.next 提供
        next_url = (
            f"https://www.zhihu.com/api/v3/moments/{user_id}/activities"
            f"?limit=5&desktop=true"
        )
        d_c0 = self._get_d_c0()
        cookies = parse_cookie_string(self.config.get("cookie", ""))
        referer = f"https://www.zhihu.com/people/{user_id}"

        activities: list[dict] = []
        async with AsyncSession() as session:
            while next_url and len(activities) < limit:
                signature = get_signature(next_url, d_c0)
                headers = {
                    "accept": "*/*",
                    "user-agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
                    "x-requested-with": "fetch",
                    "x-zse-93": signature["x_zse_93"],
                    "x-zse-96": signature["x_zse_96"],
                    "referer": referer,
                }
                resp = await session.get(next_url, headers=headers, cookies=cookies)
                if resp.status_code != 200:
                    raise RuntimeError(f"知乎 API 错误: {resp.status_code}")

                data = resp.json()
                activities.extend(data.get("data", []))

                paging = data.get("paging", {})
                if paging.get("is_end"):
                    break
                next_url = paging.get("next")

        return activities[:limit]

    async def fetch(self, article_store=None, **kwargs) -> list[FeedItem]:
        path_params: list[str] = kwargs.get("path_params", [])
        if not path_params:
            raise ValueError("需要指定用户 ID")

        user_id = path_params[0]
        limit = int(kwargs.get("limit", 20))

        # 优先从 kwargs 获取（refresher 透传），其次从 self.config 查找
        include = kwargs.get("include")
        if include is None:
            include = self._get_feed_include(user_id)

        logger.info(f"开始抓取知乎用户 {user_id}，limit={limit}")

        activities = await self._fetch_activities(user_id, limit)

        items = []
        for act in activities:
            if not act.get("target"):
                continue
            category = _derive_category(act)
            if include and category not in include:
                continue
            items.append(self._make_feed_item(act))

        logger.info(
            f"抓取完成 {user_id}: 累计 {len(activities)} 条动态，过滤后 {len(items)} 条"
        )
        return items
