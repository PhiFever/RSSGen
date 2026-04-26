"""路由基类与数据模型"""

from dataclasses import dataclass, field
from datetime import datetime


@dataclass
class FeedItem:
    title: str
    link: str
    content: str  # HTML 正文
    pub_date: datetime | None = None
    author: str | None = None
    guid: str | None = None  # 默认用 link
    enclosures: list[dict] = field(default_factory=list)


@dataclass
class FeedInfo:
    title: str
    link: str
    description: str


class Route:
    """路由基类，每个数据源继承此类"""

    name: str = ""
    description: str = ""

    def __init__(self, config: dict):
        self.config = config

    async def feed_info(self, **kwargs) -> FeedInfo:
        raise NotImplementedError

    async def fetch(
        self, article_cache=None, article_store=None, **kwargs
    ) -> list[FeedItem]:
        """抓取数据源。

        参数:
            article_cache: 可选的内存型缓存（key-value），适合临时性数据源
            article_store: 可选的持久化存储（ArticleStoreProtocol），适合需要跨重启
                保留已抓内容的数据源
        """
        raise NotImplementedError
