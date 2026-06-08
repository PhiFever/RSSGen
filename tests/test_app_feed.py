"""feed 端点：feed 级禁用返回 502"""

from unittest.mock import MagicMock

import pytest
from fastapi import HTTPException

import RSSGen.app as app_module
from RSSGen.core.notifier import Notifier


@pytest.mark.asyncio
async def test_disabled_feed_returns_502(monkeypatch):
    """被禁用的 feed 访问时返回 HTTP 502"""
    notifier = Notifier({})
    notifier.disable_feed("afdian/author1")
    monkeypatch.setattr(app_module, "notifier", notifier)
    monkeypatch.setattr(app_module, "get_registry", lambda: {"afdian": MagicMock()})

    with pytest.raises(HTTPException) as exc_info:
        await app_module.feed("afdian", "author1", MagicMock())
    assert exc_info.value.status_code == 502


@pytest.mark.asyncio
async def test_sibling_feed_not_blocked_by_disabled_feed(monkeypatch):
    """同路由下未被禁用的 feed 不应因禁用检查被拦截"""
    notifier = Notifier({})
    notifier.disable_feed("afdian/author1")
    monkeypatch.setattr(app_module, "notifier", notifier)
    monkeypatch.setattr(app_module, "get_registry", lambda: {"afdian": MagicMock()})

    # 让 feed_cache 命中以提前返回，避免走真实 fetch
    fake_cache = MagicMock()

    async def fake_get(key):
        return "<feed/>"

    fake_cache.get = fake_get
    monkeypatch.setattr(app_module, "feed_cache", fake_cache)

    resp = await app_module.feed("afdian", "author2", MagicMock())
    assert resp.status_code == 200
