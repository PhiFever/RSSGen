"""FastAPI 主服务"""

import logging

from fastapi import FastAPI, HTTPException, Request
from fastapi.responses import Response

from RSSGen.config import load_config
from RSSGen.core.cache import Cache
from RSSGen.core.feed import generate_feed
from RSSGen.core.refresher import BackgroundRefresher
from RSSGen.routes import discover_routes, get_registry

logger = logging.getLogger("rssgen")

app = FastAPI(title="RSSGen", description="自托管 RSS 源生成框架")

# 双层缓存 + 后台刷新器
feed_cache = Cache()     # TTL 在 startup 中根据配置重新初始化
article_cache = Cache()  # TTL 在 startup 中根据配置重新初始化
refresher: BackgroundRefresher | None = None
config: dict = {}


@app.on_event("startup")
async def startup():
    global config, feed_cache, article_cache, refresher
    config = load_config()
    discover_routes()
    route_names = list(get_registry().keys())
    logger.info(f"已加载路由: {route_names}")

    # 根据配置初始化双层缓存
    afdian_config = config.get("routes", {}).get("afdian", {})
    feed_ttl = afdian_config.get("feed_ttl", 21600)
    article_ttl = afdian_config.get("article_ttl", 43200)
    feed_cache = Cache(ttl=feed_ttl)
    article_cache = Cache(ttl=article_ttl)

    # 启动后台刷新器（传入全局 config）
    if afdian_config.get("enabled", False):
        refresher = BackgroundRefresher(feed_cache, article_cache, config)
        await refresher.start()


@app.on_event("shutdown")
async def shutdown():
    global refresher
    if refresher:
        await refresher.stop()


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

    path_parts = path.strip("/").split("/") if path.strip("/") else []
    cache_key = BackgroundRefresher.build_cache_key(route_name, path_parts)

    # 1. 检查 feed 缓存（所有路由共用）
    cached = await feed_cache.get(cache_key)
    if cached:
        return Response(content=cached, media_type="application/xml; charset=utf-8")

    # 2. 解析参数
    kwargs = {}
    if path_parts:
        kwargs["path_params"] = path_parts
    kwargs.update(request.query_params)

    # 3. 如果有后台刷新器且该路由已启用，走异步模式
    if refresher:
        route_config = config.get("routes", {}).get(route_name, {})
        if route_config.get("enabled", False):
            # 触发后台刷新（非阻塞）
            await refresher.trigger(route_name, path_parts, dict(request.query_params))

            # 返回合法的空 feed
            route = route_cls(route_config)
            info = await route.feed_info(**kwargs)
            fmt = request.query_params.get("format", "atom")
            xml = generate_feed(info, [], format=fmt)
            return Response(content=xml, media_type="application/xml; charset=utf-8")

    # 4. 其他路由或未启用后台刷新，走原有同步逻辑
    route_config = config.get("routes", {}).get(route_name, {})
    route = route_cls(route_config)

    try:
        info = await route.feed_info(**kwargs)
        items = await route.fetch(article_cache=article_cache, **kwargs)
    except Exception as e:
        logger.exception(f"路由 {route_name} 抓取失败")
        raise HTTPException(status_code=502, detail=f"抓取失败: {e}")

    fmt = request.query_params.get("format", "atom")
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
