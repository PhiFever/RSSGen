"""测试 feed 生成"""

from datetime import datetime, timezone

from RSSGen.core.feed import generate_feed
from RSSGen.core.route import FeedInfo, FeedItem


def _make_info():
    return FeedInfo(
        title="测试 Feed",
        link="https://example.com",
        description="测试描述",
    )


def test_category_appears_in_atom_output():
    item = FeedItem(
        title="测试条目",
        link="https://example.com/1",
        guid="1",
        pub_date=datetime(2024, 1, 1, tzinfo=timezone.utc),
        categories=["answer"],
    )
    xml = generate_feed(_make_info(), [item], format="atom")
    assert 'term="answer"' in xml


def test_multiple_categories_appear_in_output():
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


def test_empty_categories_produces_no_category_tag():
    item = FeedItem(
        title="测试条目",
        link="https://example.com/1",
        guid="1",
        pub_date=datetime(2024, 1, 1, tzinfo=timezone.utc),
    )
    xml = generate_feed(_make_info(), [item], format="atom")
    assert "<category" not in xml
