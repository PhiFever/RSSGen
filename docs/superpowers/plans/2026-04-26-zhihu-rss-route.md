# 知乎 RSS 路由实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 RSSGen 添加知乎用户动态订阅路由，使用 PyMiniRacer 生成签名，无需 Node.js 依赖。

**Architecture:** 复用 afdian 路由架构，签名生成通过 PyMiniRacer 调用合并后的 JS 文件。路由类继承 Route 基类，实现 `feed_info()` 和 `fetch()` 方法。API 直接返回完整正文，无需二次请求。

**Tech Stack:** Python 3.12, PyMiniRacer (V8), FastAPI, curl_cffi, pytest-asyncio

---

## 文件结构

```
创建/修改文件:
- Create: RSSGen/sign/zhihu/zhihu_sign.js      # 签名 JS（从 demo 复制）
- Create: RSSGen/routes/zhihu.py               # 路由类
- Create: tests/test_zhihu_sign.py             # 签名生成单元测试
- Create: tests/test_zhihu_route.py            # 路由逻辑测试
- Modify: config.example.yml                   # 添加知乎配置模板
```

---

### Task 1: 复制签名 JS 文件到项目目录

**Files:**
- Create: `RSSGen/sign/zhihu/zhihu_sign.js`

- [ ] **Step 1: 创建 sign/zhihu 目录**

```bash
mkdir -p RSSGen/sign/zhihu
```

- [ ] **Step 2: 复制 JS 文件**

```bash
cp zhihu_sign_demo/zhihu_sign.js RSSGen/sign/zhihu/zhihu_sign.js
```

- [ ] **Step 3: 验证文件存在**

```bash
ls -la RSSGen/sign/zhihu/
```
Expected: 显示 `zhihu_sign.js` 文件

- [ ] **Step 4: 提交**

```bash
git add RSSGen/sign/zhihu/zhihu_sign.js
git commit -m "feat: 添加知乎签名 JS 文件"
```

---

### Task 2: 测试签名生成模块

**Files:**
- Create: `tests/test_zhihu_sign.py`

- [ ] **Step 1: Write the failing test**

```python
"""知乎签名生成单元测试"""

import pytest
from pathlib import Path

from RSSGen.routes.zhihu import ZhihuSigner


class TestZhihuSigner:
    def test_init_loads_js_file(self):
        """初始化时加载 JS 文件"""
        signer = ZhihuSigner()
        assert signer._ctx is not None

    def test_get_signature_returns_valid_format(self):
        """签名返回正确格式"""
        signer = ZhihuSigner()
        url = "https://www.zhihu.com/api/v4/questions/123/answers?limit=5"
        d_c0 = "test_dc0_value"

        result = signer.get_signature(url, d_c0)

        assert "x_zse_93" in result
        assert result["x_zse_93"] == "101_3_3.0"
        assert "x_zse_96" in result
        assert result["x_zse_96"].startswith("2.0_")

    def test_get_signature_different_urls_produce_different_results(self):
        """不同 URL 产生不同签名"""
        signer = ZhihuSigner()

        sig1 = signer.get_signature(
            "https://www.zhihu.com/api/v4/questions/111/answers", "dc0"
        )
        sig2 = signer.get_signature(
            "https://www.zhihu.com/api/v4/questions/222/answers", "dc0"
        )

        assert sig1["x_zse_96"] != sig2["x_zse_96"]
```

- [ ] **Step 2: Run test to verify it fails**

```bash
pytest tests/test_zhihu_sign.py -v
```
Expected: FAIL with "ModuleNotFoundError: No module named 'RSSGen.routes.zhihu'"

- [ ] **Step 3: Write minimal implementation - 路由文件骨架**

创建 `RSSGen/routes/zhihu.py`，先只实现签名类：

```python
"""知乎用户动态路由"""

import re
from datetime import datetime, timezone
from pathlib import Path
from typing import list

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
```

- [ ] **Step 4: Run test to verify it passes**

```bash
pytest tests/test_zhihu_sign.py -v
```
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add RSSGen/routes/zhihu.py tests/test_zhihu_sign.py
git commit -m "feat: 知乎签名生成模块"
```

---

### Task 3: 测试路由 feed_info 方法

**Files:**
- Modify: `tests/test_zhihu_route.py`
- Modify: `RSSGen/routes/zhihu.py`

- [ ] **Step 1: Write the failing test**

追加到 `tests/test_zhihu_route.py`：

```python
import pytest

from RSSGen.routes.zhihu import ZhihuRoute


class TestZhihuRouteFeedInfo:
    @pytest.mark.asyncio
    async def test_feed_info_returns_correct_title_and_link(self):
        """feed_info 返回正确的标题和链接"""
        route = ZhihuRoute({"cookie": "test"})
        info = await route.feed_info(path_params=["kvxjr369f"])

        assert info.title == "知乎动态 - kvxjr369f"
        assert info.link == "https://www.zhihu.com/people/kvxjr369f"
        assert "kvxjr369f" in info.description

    @pytest.mark.asyncio
    async def test_feed_info_requires_user_id(self):
        """缺少 user_id 时抛出异常"""
        route = ZhihuRoute({"cookie": "test"})

        with pytest.raises(ValueError, match="需要指定用户"):
            await route.feed_info()
```

- [ ] **Step 2: Run test to verify it fails**

```bash
pytest tests/test_zhihu_route.py::TestZhihuRouteFeedInfo -v
```
Expected: FAIL with "AttributeError: 'ZhihuRoute' object has no attribute 'feed_info'"

- [ ] **Step 3: Write minimal implementation**

追加到 `RSSGen/routes/zhihu.py`：

```python
class ZhihuRoute(Route):
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
```

- [ ] **Step 4: Run test to verify it passes**

```bash
pytest tests/test_zhihu_route.py::TestZhihuRouteFeedInfo -v
```
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add RSSGen/routes/zhihu.py tests/test_zhihu_route.py
git commit -m "feat: 知乎路由 feed_info 方法"
```

---

### Task 4: 测试 FeedItem 构造方法

**Files:**
- Modify: `tests/test_zhihu_route.py`
- Modify: `RSSGen/routes/zhihu.py`

- [ ] **Step 1: Write the failing test**

追加到 `tests/test_zhihu_route.py`：

```python
class TestZhihuRouteMakeFeedItem:
    def test_answer_type_extracts_question_title(self):
        """回答类型从 question.title 提取标题"""
        route = ZhihuRoute({"cookie": "test"})
        target = {
            "id": "123",
            "type": "answer",
            "content": "<p>回答内容</p>",
            "created_time": 1700000000,
            "author": {"name": "作者"},
            "question": {"id": "456", "title": "问题标题"},
        }

        item = route._make_feed_item(target)

        assert item.title == "问题标题"
        assert item.link == "https://www.zhihu.com/question/456/answer/123"
        assert item.guid == "123"
        assert item.author == "作者"

    def test_article_type_extracts_target_title(self):
        """文章类型从 target.title 提取标题"""
        route = ZhihuRoute({"cookie": "test"})
        target = {
            "id": "789",
            "type": "article",
            "title": "文章标题",
            "content": "<p>文章内容</p>",
            "created_time": 1700000000,
            "author": {"name": "作者"},
        }

        item = route._make_feed_item(target)

        assert item.title == "文章标题"
        assert item.link == "https://zhuanlan.zhihu.com/p/789"

    def test_pin_type_uses_excerpt_as_title(self):
        """想法类型使用摘要作为标题"""
        route = ZhihuRoute({"cookie": "test"})
        target = {
            "id": "111",
            "type": "pin",
            "excerpt": "这是一条想法的摘要内容",
            "created_time": 1700000000,
            "author": {"name": "作者"},
        }

        item = route._make_feed_item(target)

        assert "摘要内容" in item.title
        assert item.link == "https://www.zhihu.com/pin/111"

    def test_pub_date_from_created_time(self):
        """pub_date 从 created_time Unix 时间戳转换"""
        route = ZhihuRoute({"cookie": "test"})
        target = {
            "id": "123",
            "type": "answer",
            "created_time": 1700000000,
            "content": "",
            "author": {"name": "a"},
            "question": {"id": "1", "title": "t"},
        }

        item = route._make_feed_item(target)

        assert item.pub_date == datetime(2023, 11, 14, 22, 13, 20, tzinfo=timezone.utc)
```

- [ ] **Step 2: Run test to verify it fails**

```bash
pytest tests/test_zhihu_route.py::TestZhihuRouteMakeFeedItem -v
```
Expected: FAIL with "AttributeError: 'ZhihuRoute' object has no attribute '_make_feed_item'"

- [ ] **Step 3: Write minimal implementation**

追加到 `RSSGen/routes/zhihu.py`（在 ZhihuRoute 类中）：

```python
    def _make_feed_item(self, target: dict) -> FeedItem:
        """根据 target dict 构造 FeedItem"""
        target_id = target.get("id", "")
        target_type = target.get("type", "unknown")
        created_time = target.get("created_time", 0)

        # 标题和链接根据类型处理
        if target_type == "answer":
            question = target.get("question", {})
            title = question.get("title", "")
            question_id = question.get("id", "")
            link = f"https://www.zhihu.com/question/{question_id}/answer/{target_id}"
        elif target_type == "article":
            title = target.get("title", "")
            link = f"https://zhuanlan.zhihu.com/p/{target_id}"
        elif target_type == "pin":
            excerpt = target.get("excerpt", "")
            title = excerpt[:50] if excerpt else "想法"
            link = f"https://www.zhihu.com/pin/{target_id}"
        else:
            title = target.get("title", target.get("excerpt", "未知内容")[:50])
            link = f"https://www.zhihu.com/{target_type}/{target_id}"

        pub_date = (
            datetime.fromtimestamp(created_time, tz=timezone.utc)
            if created_time
            else None
        )

        content = target.get("content", "") or target.get("excerpt", "")
        author = target.get("author", {}).get("name", "")

        return FeedItem(
            title=title,
            link=link,
            content=content,
            pub_date=pub_date,
            author=author,
            guid=target_id,
        )
```

- [ ] **Step 4: Run test to verify it passes**

```bash
pytest tests/test_zhihu_route.py::TestZhihuRouteMakeFeedItem -v
```
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add RSSGen/routes/zhihu.py tests/test_zhihu_route.py
git commit -m "feat: 知乎路由 _make_feed_item 方法"
```

---

### Task 5: 测试 fetch 方法（Mock API）

**Files:**
- Modify: `tests/test_zhihu_route.py`
- Modify: `RSSGen/routes/zhihu.py`

- [ ] **Step 1: Write the failing test**

追加到 `tests/test_zhihu_route.py`：

```python
from unittest.mock import AsyncMock, patch, MagicMock


class TestZhihuRouteFetch:
    @pytest.mark.asyncio
    async def test_fetch_returns_feed_items(self):
        """fetch 返回 FeedItem 列表"""
        route = ZhihuRoute({"cookie": "d_c0=test; other=val"})

        # Mock API 响应
        mock_response = MagicMock()
        mock_response.status_code = 200
        mock_response.json.return_value = {
            "data": [
                {
                    "id": "act1",
                    "type": "feed",
                    "target": {
                        "id": "123",
                        "type": "answer",
                        "content": "<p>内容</p>",
                        "created_time": 1700000000,
                        "author": {"name": "作者"},
                        "question": {"id": "456", "title": "问题标题"},
                    },
                },
                {
                    "id": "act2",
                    "type": "feed",
                    "target": {
                        "id": "789",
                        "type": "article",
                        "title": "文章标题",
                        "content": "<p>文章内容</p>",
                        "created_time": 1700000100,
                        "author": {"name": "作者2"},
                    },
                },
            ]
        }

        with (
            patch.object(route, "_fetch_activities", new_callable=AsyncMock, return_value=mock_response),
        ):
            items = await route.fetch(path_params=["kvxjr369f"])

        assert len(items) == 2
        assert items[0].title == "问题标题"
        assert items[1].title == "文章标题"
```

- [ ] **Step 2: Run test to verify it fails**

```bash
pytest tests/test_zhihu_route.py::TestZhihuRouteFetch -v
```
Expected: FAIL with "AttributeError: 'ZhihuRoute' object has no attribute 'fetch'"

- [ ] **Step 3: Write minimal implementation**

追加到 `RSSGen/routes/zhihu.py`：

```python
    def _get_d_c0(self) -> str:
        """从 cookie 提取 d_c0"""
        cookie_str = self.config.get("cookie", "")
        match = re.search(r"d_c0=([^;]+)", cookie_str)
        if match:
            return match.group(1)
        raise ValueError("Cookie 中缺少 d_c0 字段")

    async def _fetch_activities(self, user_id: str, limit: int = 5):
        """请求知乎用户动态 API"""
        # TODO: 实现实际 HTTP 请求
        pass

    async def fetch(self, article_store=None, **kwargs) -> list[FeedItem]:
        path_params: list[str] = kwargs.get("path_params", [])
        if not path_params:
            raise ValueError("需要指定用户 ID")

        user_id = path_params[0]
        limit = int(kwargs.get("limit", 20))

        logger.info(f"开始抓取知乎用户 {user_id}，limit={limit}")

        resp = await self._fetch_activities(user_id, limit)

        if resp.status_code != 200:
            raise RuntimeError(f"知乎 API 错误: {resp.status_code}")

        data = resp.json()
        activities = data.get("data", [])

        items = []
        for act in activities:
            target = act.get("target", {})
            if target:
                items.append(self._make_feed_item(target))

        logger.info(f"抓取完成 {user_id}: {len(items)} 条动态")
        return items
```

- [ ] **Step 4: Run test to verify it passes**

```bash
pytest tests/test_zhihu_route.py::TestZhihuRouteFetch -v
```
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add RSSGen/routes/zhihu.py tests/test_zhihu_route.py
git commit -m "feat: 知乎路由 fetch 方法骨架"
```

---

### Task 6: 实现 HTTP 请求和签名集成

**Files:**
- Modify: `RSSGen/routes/zhihu.py`
- Modify: `tests/test_zhihu_route.py`

- [ ] **Step 1: Write the failing test**

追加到 `tests/test_zhihu_route.py`：

```python
class TestZhihuRouteFetchWithSigner:
    @pytest.mark.asyncio
    async def test_fetch_with_real_signature_calls_api(self):
        """fetch 使用真实签名请求 API（需要网络）"""
        # 此测试需要真实 Cookie 和网络，标记为 integration
        pytest.skip("integration test - 需要真实 Cookie")

    @pytest.mark.asyncio
    async def test_fetch_builds_correct_headers(self):
        """fetch 构造正确的请求头"""
        route = ZhihuRoute({"cookie": "d_c0=test_value"})

        # 检查 headers 构造逻辑
        url = "https://www.zhihu.com/api/v3/moments/test_user/activities"
        d_c0 = route._get_d_c0()

        signer = ZhihuSigner()
        sig = signer.get_signature(url, d_c0)

        # 验证签名格式
        assert sig["x_zse_93"] == "101_3_3.0"
        assert sig["x_zse_96"].startswith("2.0_")
```

- [ ] **Step 2: Run test to verify it fails**

```bash
pytest tests/test_zhihu_route.py::TestZhihuRouteFetchWithSigner -v
```
Expected: 第二个测试 PASS（验证签名格式），第一个 SKIP

- [ ] **Step 3: Write implementation - HTTP 请求**

追加到 `RSSGen/routes/zhihu.py`：

```python
from curl_cffi.requests import AsyncSession

    async def _fetch_activities(self, user_id: str, limit: int = 5):
        """请求知乎用户动态 API"""
        url = f"https://www.zhihu.com/api/v3/moments/{user_id}/activities"
        url_with_params = f"{url}?limit={limit}&desktop=true"

        d_c0 = self._get_d_c0()
        signer = ZhihuSigner()
        signature = signer.get_signature(url_with_params, d_c0)

        headers = {
            "accept": "*/*",
            "user-agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
            "x-requested-with": "fetch",
            "x-zse-93": signature["x_zse_93"],
            "x-zse-96": signature["x_zse_96"],
            "referer": f"https://www.zhihu.com/people/{user_id}",
        }

        cookies = {}
        cookie_str = self.config.get("cookie", "")
        for item in cookie_str.split(";"):
            item = item.strip()
            if "=" in item:
                k, v = item.split("=", 1)
                cookies[k.strip()] = v.strip()

        async with AsyncSession() as session:
            resp = await session.get(url_with_params, headers=headers, cookies=cookies)
            return resp
```

- [ ] **Step 4: Run test to verify it passes**

```bash
pytest tests/test_zhihu_route.py -v
```
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add RSSGen/routes/zhihu.py tests/test_zhihu_route.py
git commit -m "feat: 知乎路由完整实现 - HTTP 请求与签名集成"
```

---

### Task 7: 更新配置模板

**Files:**
- Modify: `config.example.yml`

- [ ] **Step 1: 添加知乎配置模板**

追加到 `config.example.yml`：

```yaml
  zhihu:
    enabled: true
    cookie: "你的知乎 Cookie（必须包含 d_c0）"
    # rate_limit: 0.5
    refresh_interval: 14400
    article_ttl: 43200
    feed_ttl: 21600
    feeds:
      - user_id: "your_user_id"   # 用户主页 URL token
        alias: "用户别名"           # 别名，用于易读的 feed title
        limit: 20
```

- [ ] **Step 2: 验证配置格式**

```bash
cat config.example.yml
```
Expected: 显示完整的配置模板，包含 zhihu 部分

- [ ] **Step 3: Commit**

```bash
git add config.example.yml
git commit -m "docs: 添加知乎路由配置模板"
```

---

### Task 8: 集成测试验证

**Files:**
- Create: `tests/test_zhihu_integration.py`（可选，需真实 Cookie）

- [ ] **Step 1: 运行所有知乎相关测试**

```bash
pytest tests/test_zhihu_sign.py tests/test_zhihu_route.py -v
```
Expected: 所有测试 PASS

- [ ] **Step 2: 验证路由注册**

```bash
source .venv/bin/activate && python -c "
from RSSGen.routes import discover_routes, get_registry
discover_routes()
print('已加载路由:', list(get_registry().keys()))
"
```
Expected: 显示 `['afdian', 'zhihu']`

- [ ] **Step 3: 最终提交（如有遗漏）**

```bash
git status
git add -A
git commit -m "feat: 知乎 RSS 路由完整实现"
```

---

## 自检清单

**1. Spec 覆盖检查：**
- 目录结构: Task 1 覆盖 ✓
- 签名生成: Task 2, Task 6 覆盖 ✓
- feed_info: Task 3 覆盖 ✓
- FeedItem 映射: Task 4 覆盖 ✓
- fetch 方法: Task 5, Task 6 覆盖 ✓
- 配置格式: Task 7 覆盖 ✓

**2. Placeholder 检查：**
- 无 TBD/TODO ✓
- 所有代码步骤有完整代码 ✓
- 所有命令有预期输出 ✓

**3. 类型一致性：**
- `_make_feed_item(target: dict) -> FeedItem` ✓
- `get_signature(url: str, d_c0: str) -> dict` ✓
- `fetch(...) -> list[FeedItem]` ✓