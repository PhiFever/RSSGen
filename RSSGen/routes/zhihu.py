"""知乎用户动态路由"""

import re
from datetime import datetime, timezone
from pathlib import Path
from typing import List

from loguru import logger
from py_mini_racer import MiniRacer

from RSSGen.core.route import FeedInfo, FeedItem, Route

# 签名 JS 文件路径
SIGN_JS_PATH = Path(__file__).parent.parent / "sign" / "zhihu" / "zhihu_sign.js"


class ZhihuSigner:
    """知乎签名生成器（PyMiniRacer V8 引擎）"""

    _ctx: MiniRacer | None = None

    def __init__(self):
        if ZhihuSigner._ctx is None:
            js_code = SIGN_JS_PATH.read_text()
            ZhihuSigner._ctx = MiniRacer()
            ZhihuSigner._ctx.eval(js_code)

    def get_signature(self, url: str, d_c0: str) -> dict:
        """生成 x-zse-96 签名"""
        result = ZhihuSigner._ctx.call(
            "tv",
            url,
            "",
            {"zse93": "101_3_3.0", "dc0": d_c0, "xZst81": None},
            ""
        )
        return {
            "x_zse_93": "101_3_3.0",
            "x_zse_96": "2.0_" + result["signature"]
        }


class ZhihuRoute(Route):
    """知乎用户动态路由"""

    name = "zhihu"
    description = "知乎用户动态订阅"

    async def feed_info(self, **kwargs) -> FeedInfo:
        path_params: list[str] = kwargs.get("path_params", [])
        if not path_params:
            raise ValueError("需要指定用户 ID，如 /feed/zhihu/{user_id}")

        user_id = path_params[0]
        return FeedInfo(
            title=f"知乎动态 - {user_id}",
            link=f"https://www.zhihu.com/people/{user_id}",
            description=f"知乎用户 {user_id} 的最新动态",
        )