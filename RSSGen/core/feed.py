"""Feed 生成：将 FeedInfo + FeedItem 列表转为 Atom/RSS XML"""

from datetime import datetime, timezone
from uuid import uuid4

from feedgen.feed import FeedGenerator
from loguru import logger

from RSSGen.core.route import FeedInfo, FeedItem

_EPOCH = datetime.fromtimestamp(0, tz=timezone.utc)


def generate_feed(info: FeedInfo, items: list[FeedItem], format: str = "atom") -> str:
    fg = FeedGenerator()
    fg.id(info.link)
    fg.title(info.title)
    fg.link(href=info.link, rel="alternate")
    fg.description(info.description)

    for i, item in enumerate(items):
        fe = fg.add_entry()

        entry_title = item.title or "无标题"
        entry_id = item.guid or item.link or f"urn:uuid:{uuid4()}"
        entry_updated = item.pub_date or _EPOCH

        # 诊断日志：记录 FeedItem 转 feedgen entry 的关键字段
        logger.debug(
            f"generate_feed entry[{i}]: title={entry_title!r}, "
            f"id={entry_id!r}, updated={entry_updated}, "
            f"pub_date={item.pub_date}, link={item.link!r}, "
            f"content_len={len(item.content or '')}"
        )

        fe.title(entry_title)
        fe.id(entry_id)
        fe.updated(entry_updated)
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

    try:
        if format == "rss":
            return fg.rss_str(pretty=True).decode("utf-8")
        return fg.atom_str(pretty=True).decode("utf-8")
    except ValueError as e:
        logger.error(
            f"feedgen 生成失败 ({format}): {e}, "
            f"info={{title={info.title!r}, link={info.link!r}}}, "
            f"item_count={len(items)}"
        )
        # 打印首尾各 3 个条目的关键字段，帮助定位问题
        for i in [0, 1, 2, -3, -2, -1]:
            if 0 <= i < len(items) or (i < 0 and abs(i) <= len(items)):
                item = items[i]
                logger.error(
                    f"  item[{i}]: title={item.title!r}, guid={item.guid!r}, "
                    f"link={item.link!r}, pub_date={item.pub_date}, "
                    f"author={item.author!r}, content_len={len(item.content or '')}"
                )
        raise
