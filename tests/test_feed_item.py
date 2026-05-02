"""测试 FeedItem categories 字段"""

from RSSGen.core.route import FeedItem


def test_feeditem_default_categories_is_empty_list():
    item = FeedItem(title="test")
    assert item.categories == []


def test_feeditem_accepts_categories():
    item = FeedItem(title="test", categories=["answer", "pin"])
    assert item.categories == ["answer", "pin"]
