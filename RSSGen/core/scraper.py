"""基于 curl_cffi 的反爬 HTTP 客户端封装，自动模拟浏览器 TLS 指纹"""

import asyncio
import time

from curl_cffi.requests import AsyncSession, Response
from curl_cffi.const import CurlOpt


class Scraper:
    def __init__(self, config: dict):
        self.cookies = config.get("cookies", {})
        self.proxy = config.get("proxy") or None
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

    async def _request(
        self, method: str, url: str, referer: str | None = None, **kwargs
    ) -> Response:
        await self._rate_limit_wait()
        headers = dict(self.extra_headers)
        if referer:
            headers["referer"] = referer
        async with AsyncSession(
            proxy=self.proxy,
            cookies=self.cookies,
            impersonate=self.impersonate,
            # 解决 curl_cffi 在已有连接时的异步请求 bug
            # 参考: https://github.com/lexiforest/curl_cffi/issues/302
            curl_options={CurlOpt.FRESH_CONNECT: True},
        ) as session:
            return await session.request(method, url, headers=headers, **kwargs)

    async def get(self, url: str, referer: str | None = None, **kwargs) -> Response:
        return await self._request("GET", url, referer=referer, **kwargs)

    async def post(self, url: str, referer: str | None = None, **kwargs) -> Response:
        return await self._request("POST", url, referer=referer, **kwargs)
