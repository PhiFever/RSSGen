"""知乎用户动态路由"""

import re
from datetime import datetime, timezone
from pathlib import Path

from bs4 import BeautifulSoup
from curl_cffi.requests import AsyncSession
from loguru import logger
from py_mini_racer import MiniRacer

from RSSGen.core.route import FeedInfo, FeedItem, Route
from RSSGen.core.utils import lookup_alias, parse_cookie_string

SIGN_JS_PATH = Path(__file__).parent.parent / "sign" / "zhihu" / "zhihu_sign.js"

# 签名版本常量
X_ZSE_93_VERSION = "101_3_3.0"
X_ZSE_96_PREFIX = "2.0_"

# 动态类型常量
TYPE_ANSWER = "answer"
TYPE_ARTICLE = "article"
TYPE_PIN = "pin"


class ZhihuSigner:
    """知乎签名生成器（PyMiniRacer V8 引擎）"""

    _ctx: MiniRacer | None = None

    def __init__(self):
        if ZhihuSigner._ctx is None:
            js_code = SIGN_JS_PATH.read_text(encoding="utf-8")
            ZhihuSigner._ctx = MiniRacer()
            ZhihuSigner._ctx.eval(js_code)

    def get_signature(self, url: str, d_c0: str) -> dict:
        """生成 x-zse-96 签名"""
        result = ZhihuSigner._ctx.call(
            "tv",
            url,
            "",
            {"zse93": X_ZSE_93_VERSION, "dc0": d_c0, "xZst81": None},
            ""
        )
        return {
            "x_zse_93": X_ZSE_93_VERSION,
            "x_zse_96": X_ZSE_96_PREFIX + result["signature"]
        }


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
        display_name = lookup_alias(self.config.get("feeds"), "user_id", user_id) or user_id
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
                    parts.append(f'<img src="{url}" />')
            elif block_type == "link_card":
                url = block.get("url", "")
                title = block.get("data_draft_title") or url
                if url:
                    parts.append(f'<a href="{url}">{title}</a>')
        return "<br/>".join(parts)

    def _make_feed_item(self, target: dict) -> FeedItem:
        """根据 target dict 构造 FeedItem"""
        target_id = target.get("id", "")
        target_type = target.get("type", "unknown")
        created_time = target.get("created_time", 0)

        if target_type == TYPE_ANSWER:
            question = target.get("question", {})
            title = question.get("title", "")
            question_id = question.get("id", "")
            link = f"https://www.zhihu.com/question/{question_id}/answer/{target_id}"
        elif target_type == TYPE_ARTICLE:
            title = target.get("title", "")
            link = f"https://zhuanlan.zhihu.com/p/{target_id}"
        elif target_type == TYPE_PIN:
            excerpt = target.get("excerpt", "")
            title = excerpt[:50] if excerpt else "想法"
            link = f"https://www.zhihu.com/pin/{target_id}"
        else:
            title = target.get("title", target.get("excerpt", "未知内容")[:50])
            link = f"https://www.zhihu.com/{target_type}/{target_id}"

        pub_date = (
            datetime.fromtimestamp(created_time, tz=timezone.utc)
            if created_time
            else None
        )

        raw_content = target.get("content")
        if isinstance(raw_content, list):
            content = self._render_pin_content(raw_content)
        else:
            content = raw_content or target.get("excerpt", "")
        # 修复懒加载图片占位符
        content = self._fix_lazy_images(content)
        author = target.get("author", {}).get("name", "")

        return FeedItem(
            title=title,
            link=link,
            content=content,
            pub_date=pub_date,
            author=author,
            guid=target_id,
        )

    def _get_d_c0(self) -> str:
        """从 cookie 提取 d_c0"""
        cookie_str = self.config.get("cookie", "")
        match = re.search(r"d_c0=([^;]+)", cookie_str)
        if match:
            return match.group(1)
        raise ValueError("Cookie 中缺少 d_c0 字段")

    async def _fetch_activities(self, user_id: str, limit: int = 5):
        """请求知乎用户动态 API"""
        url = f"https://www.zhihu.com/api/v3/moments/{user_id}/activities"
        url_with_params = f"{url}?limit={limit}&desktop=true"

        d_c0 = self._get_d_c0()
        signer = ZhihuSigner()
        signature = signer.get_signature(url_with_params, d_c0)

        headers = {
            "accept": "*/*",
            "user-agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
            "x-requested-with": "fetch",
            "x-zse-93": signature["x_zse_93"],
            "x-zse-96": signature["x_zse_96"],
            "referer": f"https://www.zhihu.com/people/{user_id}",
        }

        cookies = parse_cookie_string(self.config.get("cookie", ""))

        async with AsyncSession() as session:
            resp = await session.get(url_with_params, headers=headers, cookies=cookies)
            return resp

    async def fetch(self, article_store=None, **kwargs) -> list[FeedItem]:
        path_params: list[str] = kwargs.get("path_params", [])
        if not path_params:
            raise ValueError("需要指定用户 ID")

        user_id = path_params[0]
        limit = int(kwargs.get("limit", 20))

        logger.info(f"开始抓取知乎用户 {user_id}，limit={limit}")

        resp = await self._fetch_activities(user_id, limit)

        if resp.status_code != 200:
            raise RuntimeError(f"知乎 API 错误: {resp.status_code}")

        data = resp.json()
        activities = data.get("data", [])

        items = []
        for act in activities:
            target = act.get("target", {})
            if target:
                items.append(self._make_feed_item(target))

        logger.info(f"抓取完成 {user_id}: {len(items)} 条动态")
        return items