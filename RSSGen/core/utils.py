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


def lookup_alias(feeds: list | None, key: str, value: str) -> str | None:
    """在 feeds 配置列表中按指定字段查找 alias

    参数:
        feeds: 路由配置中的 feeds 列表，每项为 dict
        key: 用于匹配的字段名（如 "user_id"、"slug"）
        value: 待匹配的字段值
    """
    for feed in feeds or []:
        if isinstance(feed, dict) and feed.get(key) == value:
            return feed.get("alias")
    return None