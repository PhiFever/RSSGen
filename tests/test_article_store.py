"""SqliteArticleStore 单元测试"""

import pytest
import pytest_asyncio

from RSSGen.core.article_store import SqliteArticleStore


@pytest_asyncio.fixture
async def store(tmp_path):
    s = SqliteArticleStore(tmp_path / "test.db")
    await s.init()
    yield s
    await s.close()


class TestInit:
    @pytest.mark.asyncio
    async def test_creates_missing_directory(self, tmp_path):
        """init 自动创建不存在的父目录"""
        nested = tmp_path / "nested" / "deeper" / "test.db"
        s = SqliteArticleStore(nested)
        await s.init()
        try:
            assert nested.parent.is_dir()
            assert nested.exists()
        finally:
            await s.close()


class TestRoundTrip:
    @pytest.mark.asyncio
    async def test_save_then_get(self, store):
        """save 后 get 能取回原内容"""
        await store.save("afdian", "post1", "<p>hello</p>")
        result = await store.get("afdian", "post1")
        assert result == "<p>hello</p>"

    @pytest.mark.asyncio
    async def test_unicode_and_html(self, store):
        """支持 Unicode、换行、HTML 标签"""
        content = "<div>中文 内容\n含<br/>换行 特殊字符</div>"
        await store.save("afdian", "post2", content)
        assert await store.get("afdian", "post2") == content

    @pytest.mark.asyncio
    async def test_get_miss_returns_none(self, store):
        """未命中返回 None"""
        assert await store.get("afdian", "nonexistent") is None


class TestKeySemantics:
    @pytest.mark.asyncio
    async def test_replace_on_duplicate(self, store):
        """同 (route, item_id) 重复 save 应覆盖旧值"""
        await store.save("afdian", "post1", "v1")
        await store.save("afdian", "post1", "v2")
        assert await store.get("afdian", "post1") == "v2"

    @pytest.mark.asyncio
    async def test_different_routes_isolated(self, store):
        """同 item_id 不同 route 互不干扰"""
        await store.save("afdian", "x", "afdian-content")
        await store.save("zhihu", "x", "zhihu-content")
        assert await store.get("afdian", "x") == "afdian-content"
        assert await store.get("zhihu", "x") == "zhihu-content"


class TestPersistence:
    @pytest.mark.asyncio
    async def test_data_survives_close_and_reopen(self, tmp_path):
        """close 后用同路径 init，之前 save 的内容仍能取回（核心需求）"""
        path = tmp_path / "persist.db"

        s1 = SqliteArticleStore(path)
        await s1.init()
        await s1.save("afdian", "post1", "<p>persistent</p>")
        await s1.close()

        s2 = SqliteArticleStore(path)
        await s2.init()
        try:
            assert await s2.get("afdian", "post1") == "<p>persistent</p>"
        finally:
            await s2.close()


class TestDegradation:
    @pytest.mark.asyncio
    async def test_get_returns_none_when_uninitialized(self, tmp_path):
        """未 init 直接 get 不抛异常，返回 None"""
        s = SqliteArticleStore(tmp_path / "x.db")
        result = await s.get("afdian", "post1")
        assert result is None

    @pytest.mark.asyncio
    async def test_save_silent_when_uninitialized(self, tmp_path):
        """未 init 直接 save 不抛异常"""
        s = SqliteArticleStore(tmp_path / "x.db")
        await s.save("afdian", "post1", "content")  # 不应抛

    @pytest.mark.asyncio
    async def test_get_after_close_returns_none(self, tmp_path):
        """close 后 get 不抛异常，返回 None"""
        s = SqliteArticleStore(tmp_path / "x.db")
        await s.init()
        await s.close()
        assert await s.get("afdian", "post1") is None

    @pytest.mark.asyncio
    async def test_save_after_close_silent(self, tmp_path):
        """close 后 save 不抛异常"""
        s = SqliteArticleStore(tmp_path / "x.db")
        await s.init()
        await s.close()
        await s.save("afdian", "post1", "content")  # 不应抛
