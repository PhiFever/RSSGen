"""Feed 生成：将 FeedInfo + FeedItem 列表转为 Atom/RSS XML"""

from datetime import datetime, timezone
from uuid import uuid4

from feedgen.feed import FeedGenerator

from RSSGen.core.route import FeedInfo, FeedItem

_EPOCH = datetime.fromtimestamp(0, tz=timezone.utc)


def generate_feed(info: FeedInfo, items: list[FeedItem], format: str = "atom") -> str:
    fg = FeedGenerator()
    fg.id(info.link)
    fg.title(info.title)
    fg.link(href=info.link, rel="alternate")
    fg.description(info.description)

    for item in items:
        fe = fg.add_entry()
        fe.title(item.title or "无标题")
        fe.id(item.guid or item.link or f"urn:uuid:{uuid4()}")
        fe.updated(item.pub_date or _EPOCH)
        if item.pub_date:
            fe.published(item.pub_date)
        if item.link:
            fe.link(href=item.link)
        if item.content:
            fe.content(item.content, type="html")
        if item.author:
            fe.author(name=item.author)
        for enc in item.enclosures:
            if enc.get("url"):
                fe.enclosure(
                    url=enc["url"],
                    length=enc.get("length", "0"),
                    type=enc.get("type", ""),
                )

    if format == "rss":
        return fg.rss_str(pretty=True).decode("utf-8")
    return fg.atom_str(pretty=True).decode("utf-8")
