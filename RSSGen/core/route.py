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

    async def fetch(self, article_cache=None, **kwargs) -> list[FeedItem]:
        """抓取数据源。

        参数:
            article_cache: 可选的文章级缓存实例，子类可利用它跳过已缓存文章的 API 调用
        """
        raise NotImplementedError
