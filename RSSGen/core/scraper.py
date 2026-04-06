"""基于 curl_cffi 的反爬 HTTP 客户端封装，自动模拟浏览器 TLS 指纹"""

import asyncio
import time

from curl_cffi.requests import AsyncSession, Response


class Scraper:
    def __init__(self, config: dict):
        self.cookies = config.get("cookies", {})
        self.proxy = config.get("proxy", "")
        self.rate_limit: float = config.get("rate_limit", 1.0)
        self.impersonate: str = config.get("impersonate", "chrome131")
        self.extra_headers: dict = config.get("extra_headers", {})
        self._last_request_time: float = 0

    async def _rate_limit_wait(self):
        now = time.monotonic()
        elapsed = now - self._last_request_time
        if elapsed < self.rate_limit:
            await asyncio.sleep(self.rate_limit - elapsed)
        self._last_request_time = time.monotonic()

    async def get(self, url: str, referer: str | None = None, **kwargs) -> Response:
        await self._rate_limit_wait()
        headers = dict(self.extra_headers)
        if referer:
            headers["referer"] = referer
        async with AsyncSession(
            proxy=self.proxy,
            cookies=self.cookies,
            impersonate=self.impersonate,
        ) as session:
            return await session.get(url, headers=headers, **kwargs)

    async def post(self, url: str, referer: str | None = None, **kwargs) -> Response:
        await self._rate_limit_wait()
        headers = dict(self.extra_headers)
        if referer:
            headers["referer"] = referer
        async with AsyncSession(
            proxy=self.proxy,
            cookies=self.cookies,
            impersonate=self.impersonate,
        ) as session:
            return await session.post(url, headers=headers, **kwargs)
