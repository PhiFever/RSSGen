"""通用工具函数"""


def parse_cookie_string(cookie_str: str) -> dict:
    """将 Cookie 字符串解析为字典"""
    cookies = {}
    if not cookie_str:
        return cookies
    for item in cookie_str.split(";"):
        item = item.strip()
        if "=" in item:
            k, v = item.split("=", 1)
            cookies[k.strip()] = v.strip()
    return cookies