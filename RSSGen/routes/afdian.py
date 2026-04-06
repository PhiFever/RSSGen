"""爱发电路由 — 参考 AfdianToMarkdown Go 项目的 API 端点"""

import asyncio
import logging
from datetime import datetime, timezone

from RSSGen.core.route import FeedInfo, FeedItem, Route
from RSSGen.core.scraper import Scraper

logger = logging.getLogger("rssgen.afdian")

HOST = "afdian.com"
HOST_URL = "https://afdian.com"


class AfdianRoute(Route):
    name = "afdian"
    description = "爱发电创作者动态订阅"

    def _get_scraper(self) -> Scraper:
        cookie_str = self.config.get("cookie", "")
        cookies = {}
        if cookie_str:
            for pair in cookie_str.split(";"):
                pair = pair.strip()
                if "=" in pair:
                    k, v = pair.split("=", 1)
                    cookies[k.strip()] = v.strip()
        return Scraper({
            "cookies": cookies,
            "rate_limit": self.config.get("rate_limit", 0.5),
            "proxy": self.config.get("proxy"),
        })

    async def _get_author_id(self, scraper: Scraper, author_slug: str) -> str:
        """通过 url_slug 获取作者 user_id"""
        # API: /api/user/get-profile-by-slug?url_slug={slug}
        api_url = f"{HOST_URL}/api/user/get-profile-by-slug?url_slug={author_slug}"
        referer = f"{HOST_URL}/a/{author_slug}"
        resp = await scraper.get(api_url, referer=referer)
        resp.raise_for_status()
        data = resp.json()
        return data["data"]["user"]["user_id"]

    async def _get_post_list(self, scraper: Scraper, user_id: str, author_slug: str,
                             per_page: int = 10, limit: int = 0) -> list[dict]:
        """获取作者动态列表（自动翻页），limit=0 表示获取全部"""
        referer = f"{HOST_URL}/a/{author_slug}"
        all_posts = []
        publish_sn = ""

        while True:
            api_url = (
                f"{HOST_URL}/api/post/get-list?"
                f"user_id={user_id}&type=old&publish_sn={publish_sn}"
                f"&per_page={per_page}&group_id=&all=1&is_public=&plan_id=&title=&name="
            )
            resp = await scraper.get(api_url, referer=referer)
            resp.raise_for_status()
            data = resp.json()

            post_list = data.get("data", {}).get("list", [])
            if not post_list:
                break

            all_posts.extend(post_list)

            if limit and len(all_posts) >= limit:
                return all_posts[:limit]

            publish_sn = post_list[-1].get("publish_sn", "")
            if not publish_sn:
                break

            await asyncio.sleep(self.config.get("rate_limit", 0.5))

        return all_posts

    async def _get_post_detail(self, scraper: Scraper, post_id: str, album_id: str = "") -> str:
        """获取文章正文 HTML"""
        # API: /api/post/get-detail?post_id={id}&album_id={id}
        api_url = f"{HOST_URL}/api/post/get-detail?post_id={post_id}&album_id={album_id}"
        referer = f"{HOST_URL}/p/{post_id}"
        resp = await scraper.get(api_url, referer=referer)
        resp.raise_for_status()
        data = resp.json()
        return data.get("data", {}).get("post", {}).get("content", "")

    async def feed_info(self, **kwargs) -> FeedInfo:
        path_params: list[str] = kwargs.get("path_params", [])
        if not path_params:
            raise ValueError("需要指定作者 url_slug，如 /feed/afdian/{author_slug}")
        author_slug = path_params[0]
        return FeedInfo(
            title=f"爱发电 - {author_slug}",
            link=f"{HOST_URL}/a/{author_slug}",
            description=f"爱发电创作者 {author_slug} 的最新动态",
        )

    async def fetch(self, **kwargs) -> list[FeedItem]:
        path_params: list[str] = kwargs.get("path_params", [])
        if not path_params:
            raise ValueError("需要指定作者 url_slug，如 /feed/afdian/{author_slug}")
        author_slug = path_params[0]

        limit = int(kwargs.get("limit", 20))

        scraper = self._get_scraper()
        user_id = await self._get_author_id(scraper, author_slug)
        posts = await self._get_post_list(scraper, user_id, author_slug, limit=limit)

        items = []
        for post in posts:
            publish_time = int(post.get("publish_time", 0))
            pub_date = datetime.fromtimestamp(publish_time, tz=timezone.utc) if publish_time else None

            # 图片列表作为 enclosures
            enclosures = []
            for pic in post.get("pics", []):
                if pic:
                    enclosures.append({"url": pic, "type": "image/jpeg"})

            post_id = post.get("post_id", "")

            # 通过详情 API 获取完整正文，列表 API 返回的 content 是截断的摘要
            content = await self._get_post_detail(scraper, post_id)

            items.append(FeedItem(
                title=post.get("title", "无标题"),
                link=f"{HOST_URL}/p/{post_id}",
                content=content,
                pub_date=pub_date,
                author=post.get("user", {}).get("name"),
                guid=post_id,
                enclosures=enclosures,
            ))

            await asyncio.sleep(self.config.get("rate_limit", 0.5))

        return items
