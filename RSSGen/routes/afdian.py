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

    @staticmethod
    def _check_api_response(data: dict, context: str):
        """检查爱发电 API 业务级响应，非 200 时抛出明确异常"""
        ec = data.get("ec", -1)
        if ec != 200:
            em = data.get("em", "未知错误")
            raise RuntimeError(f"爱发电 API 错误 ({context}): ec={ec}, em={em}")

    async def _get_author_id(self, scraper: Scraper, author_slug: str) -> str:
        """通过 url_slug 获取作者 user_id"""
        # API: /api/user/get-profile-by-slug?url_slug={slug}
        logger.info(f"获取作者 ID: {author_slug}")
        api_url = f"{HOST_URL}/api/user/get-profile-by-slug?url_slug={author_slug}"
        referer = f"{HOST_URL}/a/{author_slug}"
        resp = await scraper.get(api_url, referer=referer)
        resp.raise_for_status()
        data = resp.json()
        self._check_api_response(data, f"get-profile-by-slug/{author_slug}")
        user_id = data["data"]["user"]["user_id"]
        logger.info(f"作者 ID: {user_id}")
        return user_id

    async def _get_post_list(self, scraper: Scraper, user_id: str, author_slug: str,
                             per_page: int = 10, limit: int = 0) -> list[dict]:
        """获取作者动态列表（自动翻页），limit=0 表示获取全部"""
        referer = f"{HOST_URL}/a/{author_slug}"
        all_posts = []
        publish_sn = ""
        page = 1

        while True:
            api_url = (
                f"{HOST_URL}/api/post/get-list?"
                f"user_id={user_id}&type=old&publish_sn={publish_sn}"
                f"&per_page={per_page}&group_id=&all=1&is_public=&plan_id=&title=&name="
            )
            resp = await scraper.get(api_url, referer=referer)
            resp.raise_for_status()
            data = resp.json()
            self._check_api_response(data, f"get-list/{author_slug}")

            post_list = data.get("data", {}).get("list", [])
            if not post_list:
                logger.info(f"列表页 {page}: 无更多数据，结束翻页")
                break

            all_posts.extend(post_list)
            logger.info(f"列表页 {page}: 获取 {len(post_list)} 条，累计 {len(all_posts)} 条")

            if limit and len(all_posts) >= limit:
                logger.info(f"已达 limit={limit}，停止翻页")
                return all_posts[:limit]

            publish_sn = post_list[-1].get("publish_sn", "")
            if not publish_sn:
                logger.info(f"列表页 {page}: 无 publish_sn，结束翻页")
                break

            page += 1
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
        self._check_api_response(data, f"get-detail/{post_id}")
        content = data.get("data", {}).get("post", {}).get("content", "")
        logger.debug(f"文章详情 {post_id}: {len(content)} 字符")
        return content

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

    async def fetch(self, article_cache=None, **kwargs) -> list[FeedItem]:
        path_params: list[str] = kwargs.get("path_params", [])
        if not path_params:
            raise ValueError("需要指定作者 url_slug，如 /feed/afdian/{author_slug}")
        author_slug = path_params[0]

        limit = int(kwargs.get("limit", 20))
        logger.info(f"开始抓取 {author_slug}，limit={limit}")

        scraper = self._get_scraper()
        user_id = await self._get_author_id(scraper, author_slug)
        posts = await self._get_post_list(scraper, user_id, author_slug, limit=limit)

        items = []
        for post in posts:
            publish_time = int(post.get("publish_time", 0))
            pub_date = datetime.fromtimestamp(publish_time, tz=timezone.utc) if publish_time else None

            enclosures = []
            for pic in post.get("pics", []):
                if pic:
                    enclosures.append({"url": pic, "type": "image/jpeg"})

            post_id = post.get("post_id", "")
            article_cache_key = f"article:afdian:{post_id}"

            # 优先读文章缓存
            cache_hit = False
            content = None
            if article_cache:
                content = await article_cache.get(article_cache_key)
                if content is not None:
                    cache_hit = True

            # 缓存未命中才调 API
            if not cache_hit:
                content = await self._get_post_detail(scraper, post_id)
                if article_cache and content:
                    await article_cache.set(article_cache_key, content)
                await asyncio.sleep(self.config.get("rate_limit", 0.5))

            items.append(FeedItem(
                title=post.get("title", "无标题"),
                link=f"{HOST_URL}/p/{post_id}",
                content=content or "",
                pub_date=pub_date,
                author=post.get("user", {}).get("name"),
                guid=post_id,
                enclosures=enclosures,
            ))

        logger.info(f"抓取完成 {author_slug}: {len(items)} 条文章")
        return items
