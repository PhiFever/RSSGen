"""缓存层：内存 TTL 缓存 + 文章存储契约"""

from typing import Protocol

from cachetools import TTLCache


class Cache:
    def __init__(self, maxsize: int = 256, ttl: int = 1800):
        self._cache = TTLCache(maxsize=maxsize, ttl=ttl)

    async def get(self, key: str) -> str | None:
        return self._cache.get(key)

    async def set(self, key: str, value: str):
        self._cache[key] = value


class ArticleStoreProtocol(Protocol):
    """文章持久化存储的契约。

    实现可以是 SQLite、内存、Redis 等任意后端。路由层只通过此接口
    访问文章正文，不关心实际存储介质。
    """

    async def get(self, route: str, item_id: str) -> str | None: ...

    async def save(self, route: str, item_id: str, content: str) -> None: ...
