"""afdian 测试共享助手函数"""


def _make_post(post_id: str):
    return {
        "post_id": post_id,
        "title": f"title-{post_id}",
        "publish_time": 1700000000,
        "pics": [],
        "user": {"name": "a"},
    }


def _iter_pages(pages):
    """返回一个调用即得 async generator 的函数，依次 yield 每一页。"""
    async def _gen(*args, **kwargs):
        for page in pages:
            yield page
    return _gen
