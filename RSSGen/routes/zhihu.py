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