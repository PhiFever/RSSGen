"""SQLite 后端的文章持久化存储"""

from __future__ import annotations

import asyncio
import time
from pathlib import Path
from typing import TYPE_CHECKING

import aiosqlite
from loguru import logger

if TYPE_CHECKING:
    from aiosqlite import Connection


_CREATE_TABLE_SQL = """
CREATE TABLE IF NOT EXISTS articles (
    route      TEXT NOT NULL,
    item_id    TEXT NOT NULL,
    content    TEXT NOT NULL,
    fetched_at INTEGER NOT NULL,
    PRIMARY KEY (route, item_id)
) WITHOUT ROWID;
"""

_PRAGMAS = (
    "PRAGMA journal_mode = WAL;",
    "PRAGMA synchronous = NORMAL;",
    "PRAGMA foreign_keys = ON;",
)


class SqliteArticleStore:
    """SQLite 单文件存储，aiosqlite 异步驱动。

    生命周期由调用方管理：startup 调 init()，shutdown 调 close()。
    所有读写串行化（单连接 + asyncio.Lock），并发量本就极小。
    运行期错误降级为 warning + 当作 cache miss；init 失败硬抛。
    """

    def __init__(self, db_path: str | Path):
        self._db_path = Path(db_path)
        self._conn: Connection | None = None
        self._lock = asyncio.Lock()

    async def init(self) -> None:
        self._db_path.parent.mkdir(parents=True, exist_ok=True)
        self._conn = await aiosqlite.connect(self._db_path)
        for pragma in _PRAGMAS:
            await self._conn.execute(pragma)
        await self._conn.execute(_CREATE_TABLE_SQL)
        await self._conn.commit()
        logger.info(f"SqliteArticleStore 初始化完成: {self._db_path}")

    async def close(self) -> None:
        if self._conn is not None:
            await self._conn.close()
            self._conn = None

    async def get(self, route: str, item_id: str) -> str | None:
        if self._conn is None:
            return None
        try:
            async with self._lock:
                async with self._conn.execute(
                    "SELECT content FROM articles WHERE route = ? AND item_id = ?",
                    (route, item_id),
                ) as cursor:
                    row = await cursor.fetchone()
            return row[0] if row else None
        except Exception as e:
            logger.warning(f"ArticleStore.get 失败 ({route}/{item_id}): {e}")
            return None

    async def save(self, route: str, item_id: str, content: str) -> None:
        if self._conn is None:
            return
        try:
            now = int(time.time())
            async with self._lock:
                await self._conn.execute(
                    "INSERT OR REPLACE INTO articles "
                    "(route, item_id, content, fetched_at) VALUES (?, ?, ?, ?)",
                    (route, item_id, content, now),
                )
                await self._conn.commit()
        except Exception as e:
            logger.warning(f"ArticleStore.save 失败 ({route}/{item_id}): {e}")
