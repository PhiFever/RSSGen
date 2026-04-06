"""基于 httpx 的反爬 HTTP 客户端封装"""

import asyncio
import random
import time

import httpx

DEFAULT_USER_AGENTS = [
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
    "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
]


class Scraper:
    def __init__(self, config: dict):
        self.cookies = config.get("cookies", {})
        self.user_agents = config.get("user_agents", DEFAULT_USER_AGENTS)
        self.proxy = config.get("proxy")
        self.rate_limit: float = config.get("rate_limit", 1.0)
        self.extra_headers: dict = config.get("extra_headers", {})
        self._last_request_time: float = 0

    def _build_headers(self, referer: str | None = None) -> dict:
        headers = {
            "accept": "application/json, text/plain, */*",
            "accept-language": "zh-CN,zh;q=0.9,en;q=0.8",
            "cache-control": "no-cache",
            "dnt": "1",
            "sec-ch-ua": '"Google Chrome";v="131", "Chromium";v="131", "Not_A Brand";v="24"',
            "sec-ch-ua-mobile": "?0",
            "sec-ch-ua-platform": '"Windows"',
            "sec-fetch-dest": "empty",
            "sec-fetch-mode": "cors",
            "sec-fetch-site": "same-origin",
            "user-agent": random.choice(self.user_agents),
        }
        if referer:
            headers["referer"] = referer
        headers.update(self.extra_headers)
        return headers

    async def _rate_limit_wait(self):
        now = time.monotonic()
        elapsed = now - self._last_request_time
        if elapsed < self.rate_limit:
            await asyncio.sleep(self.rate_limit - elapsed)
        self._last_request_time = time.monotonic()

    async def get(self, url: str, referer: str | None = None, **kwargs) -> httpx.Response:
        await self._rate_limit_wait()
        async with httpx.AsyncClient(proxy=self.proxy, cookies=self.cookies) as client:
            return await client.get(url, headers=self._build_headers(referer), **kwargs)

    async def post(self, url: str, referer: str | None = None, **kwargs) -> httpx.Response:
        await self._rate_limit_wait()
        async with httpx.AsyncClient(proxy=self.proxy, cookies=self.cookies) as client:
            return await client.post(url, headers=self._build_headers(referer), **kwargs)
