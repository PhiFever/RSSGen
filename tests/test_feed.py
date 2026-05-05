"""测试 generate_feed 的兜底处理与 category 输出"""

from datetime import datetime, timezone

from RSSGen.core.feed import generate_feed
from RSSGen.core.route import FeedInfo, FeedItem


def _make_info():
    return FeedInfo(
        title="测试 Feed",
        link="https://example.com/feed",
        description="测试描述",
    )


def _parse_atom(xml: str) -> dict:
    """解析 Atom XML 返回 entries 列表"""
    import xml.etree.ElementTree as ET

    root = ET.fromstring(xml)
    entries = []
    for entry in root.findall("{http://www.w3.org/2005/Atom}entry"):
        title = entry.find("{http://www.w3.org/2005/Atom}title")
        id_elem = entry.find("{http://www.w3.org/2005/Atom}id")
        updated = entry.find("{http://www.w3.org/2005/Atom}updated")
        entries.append(
            {
                "title": title.text if title is not None else None,
                "id": id_elem.text if id_elem is not None else None,
                "updated": updated.text if updated is not None else None,
            }
        )
    return {"entries": entries}


class TestFeedFallbacks:
    """测试 generate_feed 对字段缺失的兜底处理"""

    def test_empty_title_fallback(self):
        """空字符串 title 应被替换为 '无标题'"""
        info = _make_info()
        items = [FeedItem(title="", link="https://example.com/1", content="test")]
        xml = generate_feed(info, items)
        parsed = _parse_atom(xml)
        assert parsed["entries"][0]["title"] == "无标题"

    def test_none_title_fallback(self):
        """None title 应被替换为 '无标题'"""
        info = _make_info()
        items = [FeedItem(title=None, link="https://example.com/1", content="test")]
        xml = generate_feed(info, items)
        parsed = _parse_atom(xml)
        assert parsed["entries"][0]["title"] == "无标题"

    def test_missing_guid_and_link_generates_uuid(self):
        """guid 和 link 都缺失时生成 UUID ID"""
        info = _make_info()
        items = [FeedItem(title="test", link=None, content="test", guid=None)]
        xml = generate_feed(info, items)
        parsed = _parse_atom(xml)
        assert parsed["entries"][0]["id"].startswith("urn:uuid:")

    def test_guid_fallback_to_link(self):
        """guid 缺失时使用 link 作为 ID"""
        info = _make_info()
        items = [FeedItem(title="test", link="https://example.com/1", content="test")]
        xml = generate_feed(info, items)
        parsed = _parse_atom(xml)
        assert parsed["entries"][0]["id"] == "https://example.com/1"

    def test_explicit_guid_preserved(self):
        """显式 guid 应被保留"""
        info = _make_info()
        items = [
            FeedItem(
                title="test",
                link="https://example.com/1",
                content="test",
                guid="custom-guid-123",
            )
        ]
        xml = generate_feed(info, items)
        parsed = _parse_atom(xml)
        assert parsed["entries"][0]["id"] == "custom-guid-123"

    def test_missing_pub_date_uses_epoch(self):
        """pub_date 缺失时使用 epoch 作为 updated"""
        info = _make_info()
        items = [FeedItem(title="test", link="https://example.com/1", content="test")]
        xml = generate_feed(info, items)
        parsed = _parse_atom(xml)
        assert parsed["entries"][0]["updated"] == "1970-01-01T00:00:00+00:00"

    def test_pub_date_preserved(self):
        """显式 pub_date 应被保留为 updated"""
        info = _make_info()
        dt = datetime(2024, 1, 1, 12, 0, 0, tzinfo=timezone.utc)
        items = [
            FeedItem(
                title="test", link="https://example.com/1", content="test", pub_date=dt
            )
        ]
        xml = generate_feed(info, items)
        parsed = _parse_atom(xml)
        assert parsed["entries"][0]["updated"] == "2024-01-01T12:00:00+00:00"

    def test_empty_enclosure_url_skipped(self):
        """空 enclosure URL 应被跳过"""
        info = _make_info()
        items = [
            FeedItem(
                title="test",
                link="https://example.com/1",
                content="test",
                enclosures=[{"url": "", "type": "image/jpeg"}],
            )
        ]
        xml = generate_feed(info, items)
        # 验证 XML 中不包含空 enclosure
        assert 'url=""' not in xml

    def test_rss_format_fallbacks(self):
        """RSS 格式同样应用兜底逻辑"""
        info = _make_info()
        items = [FeedItem(title="", link="https://example.com/1", content="test")]
        xml = generate_feed(info, items, format="rss")
        # RSS 的 title 元素
        assert "<title>无标题</title>" in xml


class TestFeedCategories:
    """测试 generate_feed 的 category 标签输出"""

    def test_category_appears_in_atom_output(self):
        """单个 category 应生成对应的 term 属性"""
        item = FeedItem(
            title="测试条目",
            link="https://example.com/1",
            guid="1",
            pub_date=datetime(2024, 1, 1, tzinfo=timezone.utc),
            categories=["answer"],
        )
        xml = generate_feed(_make_info(), [item], format="atom")
        assert 'term="answer"' in xml

    def test_multiple_categories_appear_in_output(self):
        """多个 category 应各自生成 term 属性"""
        item = FeedItem(
            title="测试条目",
            link="https://example.com/1",
            guid="1",
            pub_date=datetime(2024, 1, 1, tzinfo=timezone.utc),
            categories=["answer", "pin"],
        )
        xml = generate_feed(_make_info(), [item], format="atom")
        assert 'term="answer"' in xml
        assert 'term="pin"' in xml

    def test_empty_categories_produces_no_category_tag(self):
        """无 category 时不应生成 category 标签"""
        item = FeedItem(
            title="测试条目",
            link="https://example.com/1",
            guid="1",
            pub_date=datetime(2024, 1, 1, tzinfo=timezone.utc),
        )
        xml = generate_feed(_make_info(), [item], format="atom")
        assert "<category" not in xml
