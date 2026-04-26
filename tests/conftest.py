"""pytest 共享 fixtures"""

import pytest
import pytest_asyncio

from RSSGen.core.article_store import SqliteArticleStore
from RSSGen.routes.afdian import AfdianRoute


@pytest.fixture
def route():
    return AfdianRoute({"cookie": "test", "rate_limit": 0})


@pytest_asyncio.fixture
async def article_store(tmp_path):
    s = SqliteArticleStore(tmp_path / "test.db")
    await s.init()
    yield s
    await s.close()
