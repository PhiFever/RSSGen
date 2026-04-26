"""FastAPI 主服务"""

import sys

from fastapi import FastAPI, HTTPException, Request
from fastapi.responses import Response
from loguru import logger

from RSSGen.config import load_config
from RSSGen.core.cache import Cache
from RSSGen.core.feed import generate_feed
from RSSGen.core.article_store import SqliteArticleStore
from RSSGen.core.refresher import BackgroundRefresher
from RSSGen.routes import discover_routes, get_registry

# 配置 loguru：移除默认 handler，添加带时间和代码行数的格式
logger.remove()
logger.add(
    sys.stdout,
    format="<green>{time:YYYY-MM-DD HH:mm:ss}</green> | <level>{level:<8}</level> | <cyan>{name}</cyan>:<cyan>{function}</cyan>:<cyan>{line}</cyan> - <level>{message}</level>",
    level="INFO",
)

app = FastAPI(title="RSSGen", description="自托管 RSS 源生成框架")

feed_cache: Cache | None = None
article_cache: Cache | None = None
article_store: SqliteArticleStore | None = None
refresher: BackgroundRefresher | None = None
config: dict = {}


@app.on_event("startup")
async def startup():
    global config, feed_cache, article_cache, article_store, refresher
    config = load_config()
    discover_routes()
    logger.info(f"已加载路由: {list(get_registry().keys())}")

    afdian_config = config.get("routes", {}).get("afdian", {})
    feed_cache = Cache(ttl=afdian_config.get("feed_ttl", 21600))
    # 保留：通用内存型 KV，不再接入 afdian，留给将来其他路由 / 测试使用
    article_cache = Cache(ttl=afdian_config.get("article_ttl", 43200))

    sqlite_path = config.get("storage", {}).get("sqlite_path", "./data/rssgen.db")
    article_store = SqliteArticleStore(sqlite_path)
    await article_store.init()

    if afdian_config.get("enabled", False):
        refresher = BackgroundRefresher(feed_cache, article_store, config)
        await refresher.start()


@app.on_event("shutdown")
async def shutdown():
    global refresher, article_store
    if refresher:
        await refresher.stop()
    if article_store:
        await article_store.close()


@app.get("/")
async def index():
    registry = get_registry()
    return {
        "name": "RSSGen",
        "routes": {
            name: cls.description for name, cls in registry.items()
        },
    }


@app.get("/feed/{route_name}/{path:path}")
async def feed(route_name: str, path: str, request: Request):
    registry = get_registry()
    route_cls = registry.get(route_name)
    if not route_cls:
        raise HTTPException(status_code=404, detail=f"路由不存在: {route_name}")

    path_parts = [p for p in path.split("/") if p]
    cache_key = BackgroundRefresher.build_cache_key(route_name, path_parts)

    cached = await feed_cache.get(cache_key)
    if cached:
        return Response(content=cached, media_type="application/xml; charset=utf-8")

    kwargs = {**request.query_params}
    if path_parts:
        kwargs["path_params"] = path_parts

    route_config = config.get("routes", {}).get(route_name, {})
    merged_config = {**config.get("scraper", {}), **route_config}
    route = route_cls(merged_config)
    fmt = request.query_params.get("format", "atom")

    if refresher and route_config.get("enabled", False):
        await refresher.trigger(route_name, path_parts, dict(request.query_params))
        info = await route.feed_info(**kwargs)
        xml = generate_feed(info, [], format=fmt)
        return Response(content=xml, media_type="application/xml; charset=utf-8")

    try:
        info = await route.feed_info(**kwargs)
        items = await route.fetch(article_store=article_store, **kwargs)
    except Exception as e:
        logger.exception(f"路由 {route_name} 抓取失败")
        raise HTTPException(status_code=502, detail=f"抓取失败: {e}")

    xml = generate_feed(info, items, format=fmt)
    await feed_cache.set(cache_key, xml)
    return Response(content=xml, media_type="application/xml; charset=utf-8")


@app.get("/status")
async def status():
    """返回后台刷新器状态"""
    if not refresher:
        return {"enabled": False, "message": "后台刷新未启用"}

    return {
        "enabled": True,
        "feeds": refresher.get_status(),
    }
