"""缓存层：内存 TTL 缓存"""

from cachetools import TTLCache


class Cache:
    def __init__(self, maxsize: int = 256, ttl: int = 1800):
        self._cache = TTLCache(maxsize=maxsize, ttl=ttl)

    async def get(self, key: str) -> str | None:
        return self._cache.get(key)

    async def set(self, key: str, value: str):
        self._cache[key] = value
