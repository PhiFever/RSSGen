"""FastAPI 主服务"""

import logging

from fastapi import FastAPI, HTTPException, Request
from fastapi.responses import Response

from RSSGen.config import load_config
from RSSGen.core.cache import Cache
from RSSGen.core.feed import generate_feed
from RSSGen.routes import discover_routes, get_registry

logger = logging.getLogger("rssgen")

app = FastAPI(title="RSSGen", description="自托管 RSS 源生成框架")

cache = Cache()
config: dict = {}


@app.on_event("startup")
async def startup():
    global config
    config = load_config()
    discover_routes()
    route_names = list(get_registry().keys())
    logger.info(f"已加载路由: {route_names}")


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

    # 构建缓存 key
    cache_key = f"{route_name}/{path}?{request.url.query}"
    cached = await cache.get(cache_key)
    if cached:
        return Response(content=cached, media_type="application/xml; charset=utf-8")

    # 实例化路由
    route_config = config.get("routes", {}).get(route_name, {})
    route = route_cls(route_config)

    # 解析路径参数
    path_parts = path.strip("/").split("/") if path.strip("/") else []
    kwargs = {}
    if path_parts:
        kwargs["path_params"] = path_parts
    # 查询参数也传入
    kwargs.update(request.query_params)

    try:
        info = await route.feed_info(**kwargs)
        items = await route.fetch(**kwargs)
    except Exception as e:
        logger.exception(f"路由 {route_name} 抓取失败")
        raise HTTPException(status_code=502, detail=f"抓取失败: {e}")

    fmt = request.query_params.get("format", "atom")
    xml = generate_feed(info, items, format=fmt)

    await cache.set(cache_key, xml)
    return Response(content=xml, media_type="application/xml; charset=utf-8")
