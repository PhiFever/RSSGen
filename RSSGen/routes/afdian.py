"""爱发电路由 — 参考 AfdianToMarkdown Go 项目的 API 端点"""

import asyncio
from datetime import datetime, timezone
from typing import AsyncIterator

from loguru import logger

from RSSGen.core.route import FeedInfo, FeedItem, Route
from RSSGen.core.scraper import Scraper

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
        return Scraper(
            {
                "cookies": cookies,
                "rate_limit": self.config.get("rate_limit", 0.5),
                "proxy": self.config.get("proxy"),
            }
        )

    @staticmethod
    def _check_api_response(data: dict, context: str):
        """检查爱发电 API 业务级响应，非 200 时抛出明确异常"""
        ec = data.get("ec")
        if ec != 200:
            em = data.get("em", "未知错误")
            raise RuntimeError(f"爱发电 API 错误 ({context}): ec={ec}, em={em}")

    async def _get_author_id(self, scraper: Scraper, author_slug: str) -> str:
        """通过 url_slug 获取作者 user_id"""
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

    async def _iter_post_list(
        self,
        scraper: Scraper,
        user_id: str,
        author_slug: str,
        per_page: int = 10,
        limit: int = 0,
    ) -> AsyncIterator[list[dict]]:
        """逐页 yield 作者动态列表。limit=0 表示获取全部。"""
        referer = f"{HOST_URL}/a/{author_slug}"
        publish_sn = ""
        page = 1
        total_yielded = 0

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
                return

            if limit and total_yielded + len(post_list) >= limit:
                chunk = post_list[: limit - total_yielded]
                total_yielded += len(chunk)
                logger.info(
                    f"列表页 {page}: 获取 {len(chunk)} 条，累计 {total_yielded} 条"
                )
                yield chunk
                logger.info(f"已达 limit={limit}，停止翻页")
                return

            total_yielded += len(post_list)
            logger.info(
                f"列表页 {page}: 获取 {len(post_list)} 条，累计 {total_yielded} 条"
            )
            yield post_list

            publish_sn = post_list[-1].get("publish_sn", "")
            if not publish_sn:
                logger.info(f"列表页 {page}: 无 publish_sn，结束翻页")
                return

            page += 1

    async def _get_post_detail(
        self, scraper: Scraper, post_id: str, album_id: str = ""
    ) -> str:
        """获取文章正文 HTML"""
        api_url = (
            f"{HOST_URL}/api/post/get-detail?post_id={post_id}&album_id={album_id}"
        )
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

    async def _fetch_one_content(
        self, scraper: Scraper, article_store, post: dict
    ) -> str:
        """单篇文章内容获取：先查 store，未命中则下 detail 并落库。"""
        post_id = post.get("post_id", "")
        if article_store:
            cached = await article_store.get("afdian", post_id)
            if cached is not None:
                return cached

        content = await self._get_post_detail(scraper, post_id)
        logger.info(f"文章详情下载成功: {post.get('title', post_id)}")
        if article_store and content:
            await article_store.save("afdian", post_id, content)
        return content

    def _make_feed_item(self, post: dict, content: str) -> FeedItem:
        """根据 post dict 与正文 content 构造 FeedItem。"""
        publish_time = int(post.get("publish_time", 0))
        pub_date = (
            datetime.fromtimestamp(publish_time, tz=timezone.utc)
            if publish_time
            else None
        )

        enclosures = []
        for pic in post.get("pics", []):
            if pic:
                enclosures.append({"url": pic, "type": "image/jpeg"})

        post_id = post.get("post_id", "")
        return FeedItem(
            title=post.get("title", ""),
            link=f"{HOST_URL}/p/{post_id}",
            content=content or "",
            pub_date=pub_date,
            author=post.get("user", {}).get("name"),
            guid=post_id,
            enclosures=enclosures,
        )

    async def fetch(self, article_store=None, **kwargs) -> list[FeedItem]:
        path_params: list[str] = kwargs.get("path_params", [])
        if not path_params:
            raise ValueError("需要指定作者 url_slug，如 /feed/afdian/{author_slug}")
        author_slug = path_params[0]

        limit = int(kwargs.get("limit", 20))
        logger.info(f"开始抓取 {author_slug}，limit={limit}")

        scraper = self._get_scraper()
        user_id = await self._get_author_id(scraper, author_slug)

        posts: list[dict] = []
        tasks: list[asyncio.Task[str]] = []

        try:
            async for page in self._iter_post_list(
                scraper, user_id, author_slug, limit=limit
            ):
                for post in page:
                    posts.append(post)
                    tasks.append(
                        asyncio.create_task(
                            self._fetch_one_content(scraper, article_store, post)
                        )
                    )
        except asyncio.CancelledError:
            for t in tasks:
                if not t.done():
                    t.cancel()
            raise
        except Exception:
            # list 翻页失败 - 但仍等已派的 detail task 完成（让 save 落地）
            if tasks:
                await asyncio.gather(*tasks, return_exceptions=True)
            raise

        try:
            contents = await asyncio.gather(*tasks, return_exceptions=True)
        except asyncio.CancelledError:
            for t in tasks:
                if not t.done():
                    t.cancel()
            raise

        items: list[FeedItem] = []
        for post, content in zip(posts, contents):
            post_id = post.get("post_id", "")
            if isinstance(content, Exception):
                logger.warning(f"文章详情获取失败，跳过 {post_id}: {content}")
                continue
            items.append(self._make_feed_item(post, content))

        logger.info(f"抓取完成 {author_slug}: {len(items)}/{len(posts)} 条文章")
        return items
