---
name: zhihu-rss-route
description: 知乎用户动态 RSS 路由设计
type: project
---

# 知乎用户动态 RSS 路由

## 目标

为 RSSGen 添加知乎用户动态订阅功能，使用 PyMiniRacer (V8) 执行签名生成，无 Node.js 依赖。

## 目录结构

```
RSSGen/
├── routes/
│   └── zhihu.py              # 路由类
├── sign/
│   └── zhihu/
│       └── zhihu_sign.js     # 合并后的签名 JS（约 10 万行）
zhihu_sign_demo/              # 独立 demo（保留）
├── demo.py                   # PyMiniRacer 测试脚本
├── README.md
├── zhihu_sign.js             # 合并后的签名 JS
├── zhihu_lite.js             # 原签名核心（供参考）
├── other.js                  # 原加密算法（供参考）
└── test_compare.py
```

**Why:** 使用 PyMiniRacer 纯 Python V8 引擎，无需 Node.js，Docker 部署无额外依赖。JS 文件合并为单文件便于加载。

## 签名方案

PyMiniRacer 是纯 Python V8 binding，自带 V8引擎，不依赖 Node.js。

```python
from py_mini_racer import MiniRacer

# 初始化 V8 并加载签名 JS（惰性初始化）
_v8_ctx = None

def get_signature(url: str, d_c0: str) -> dict:
    global _v8_ctx
    if _v8_ctx is None:
        js_code = Path("zhihu_sign.js").read_text()
        _v8_ctx = MiniRacer()
        _v8_ctx.eval(js_code)

    result = _v8_ctx.call(
        "tv",url, "",
        {"zse93": "101_3_3.0", "dc0": d_c0, "xZst81": None},
        ""
    )
    return {
        "x_zse_93": "101_3_3.0",
        "x_zse_96": "2.0_" + result["signature"]
    }
```

**JS 环境适配：**
- `global` → `globalThis`（V8 标准）
- `URL` 类 → 手动解析（V8 无 Web API）
- `atob/btoa` → 纯 JS 实现（字节码解码需要）

## 配置格式

```yaml
routes:
  zhihu:
    enabled: true
    cookie: "你的知乎 Cookie（必须包含 d_c0）"
    feeds:
      - user_id: "kvxjr369f"   # 用户主页 URL token
        alias: "q9adg"         # 别名，用于易读的 feed title
        limit: 20
```

**Why:** `alias` 解决用户 ID 不易识别问题，如 `kvxjr369f` 对应 `q9adg`，feed title 显示为 `知乎动态 - q9adg`。

## API 端点

```
GET https://www.zhihu.com/api/v3/moments/{user_id}/activities?limit=5&desktop=true
```

**Headers:**
- `x-zse-93`: `101_3_3.0`
- `x-zse-96`: `2.0_{signature}`（PyMiniRacer 签名生成）
- `referer`: `https://www.zhihu.com/people/{user_id}`
- Cookie 中的 `d_c0` 用于签名计算

## FeedItem 映射

| target.type | title 来源 | link 构造 |
|-------------|-----------|-----------|
| `answer` | `target.question.title` | `https://www.zhihu.com/question/{question.id}/answer/{target.id}` |
| `article` | `target.title` | `https://zhuanlan.zhihu.com/p/{target.id}` |
| `pin` | 摘要前 50 字 | `https://www.zhihu.com/pin/{target.id}` |

通用字段：
- `content`: `target.content`（完整 HTML 正文，API 已返回）
- `pub_date`: `target.created_time`（Unix 时间戳）
- `author`: `target.author.name`
- `guid`: `target.id`

**Why:** API 直接返回完整正文，无需二次请求详情接口，比 afdian 路由更简单。

## 路由 URL

```
/feed/zhihu/{user_id}
```

示例：`/feed/zhihu/kvxjr369f`

## 实现步骤

1. 将 `zhihu_sign.js` 复制到 `RSSGen/sign/zhihu/`
2. 实现 `RSSGen/routes/zhihu.py` 路由类（复用 demo.py 的签名逻辑）
3. 更新 `config.example.yml` 添加知乎配置模板
4. 测试签名生成与 API 请求