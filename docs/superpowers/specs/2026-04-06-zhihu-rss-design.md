# 知乎用户动态 RSS 路由设计

## 概述

为 RSSGen 新增知乎用户动态订阅路由，用户提供知乎 `url_token`（如 `kvxjr369f`），即可将该用户的公开动态转为 RSS 订阅源。支持按动作类型过滤（回答、文章、收藏、赞同等）。

## 技术方案

### 数据获取：Playwright + js-initialData

知乎 API 需要 `x-zse-96` 签名头，该签名由前端混淆 JS 生成，无现成 Python 库，且知乎定期更新算法，维护成本高。

**采用方案**：通过 Playwright 远程连接 browserless 容器，渲染知乎用户主页，从 `<script id="js-initialData">` 标签中提取 JSON 数据。该 JSON 包含首屏动态列表，结构与 API 返回一致，无需处理签名。

```
Playwright 连接 browserless 容器（CDP）
  → 创建浏览器上下文，注入 Cookie
  → 访问 https://www.zhihu.com/people/{url_token}
  → 等待页面加载
  → 提取 <script id="js-initialData"> 中的 JSON
  → 解析 activities 列表
  → 按 verb 过滤，转为 FeedItem
  → feedgen 生成 Atom XML
```

不需要翻页，RSS 只取首屏动态，Miniflux 定时拉取实现增量更新。

### Playwright 容器

使用 `browserless/chrome` 作为独立容器，在 docker-compose.yml 中配置：

```yaml
browserless:
  image: browserless/chrome:latest
  container_name: browserless
  restart: unless-stopped
  environment:
    - MAX_CONCURRENT_SESSIONS=5
    - CONNECTION_TIMEOUT=60000
```

- 支持 AMD64 + ARM64
- 提供开箱即用的 CDP WebSocket 端点 `ws://browserless:3000`
- 内置会话队列、超时、并发管理
- RSSGen 容器 `depends_on` 添加 `browserless`

### 新增模块：core/browser.py

封装远程浏览器通用操作，供知乎路由及未来需要 JS 渲染的路由复用：

- 远程 CDP 连接管理（endpoint 可配置）
- Cookie 注入
- 页面访问与等待
- 页面数据提取

配置项（config.yml）：

```yaml
browser:
  endpoint: "ws://browserless:3000"
```

## 路由接口

### URL

```
GET /feed/zhihu/{url_token}?filter=answer,article
```

- `url_token`（路径参数）：知乎用户标识
- `filter`（可选查询参数）：逗号分隔的动作类型过滤器，不传则返回全部动态

### 过滤映射

| filter 值 | 对应 verb | 说明 |
|---|---|---|
| `answer` | `MEMBER_ANSWER_QUESTION` | 回答问题 |
| `article` | `MEMBER_CREATE_ARTICLE` | 发布文章 |
| `collect` | `MEMBER_COLLECT_ANSWER`, `MEMBER_COLLECT_ARTICLE` | 收藏 |
| `voteup` | `MEMBER_VOTEUP_ANSWER`, `MEMBER_VOTEUP_ARTICLE` | 赞同 |

### FeedItem 映射

| FeedItem 字段 | 数据来源 |
|---|---|
| `title` | `[action_text] question.title` 如 `[回答了问题] 你有哪些话想对知乎上关注你的人说？` |
| `link` | `https://www.zhihu.com/question/{question.id}/answer/{target.id}` |
| `content` | `target.content`（HTML 正文，列表 API 已包含完整内容） |
| `pub_date` | `created_time` 时间戳 |
| `author` | `target.author.name` |
| `guid` | 活动 `id` |

### FeedInfo

| 字段 | 值 |
|---|---|
| `title` | `知乎 - {url_token}` |
| `link` | `https://www.zhihu.com/people/{url_token}` |
| `description` | `知乎用户 {url_token} 的最新动态` |

## 配置

config.yml 新增：

```yaml
zhihu:
  cookie: "z_c0=...; d_c0=..."

browser:
  endpoint: "ws://browserless:3000"
```

## 新增/修改文件清单

| 文件 | 操作 | 说明 |
|---|---|---|
| `RSSGen/core/browser.py` | 新增 | Playwright 远程浏览器封装 |
| `RSSGen/routes/zhihu.py` | 新增 | 知乎用户动态路由 |
| `docker-compose.yml` | 修改 | 添加 browserless 容器，rssgen depends_on 添加 browserless |
| `config.example.yml` | 修改 | 添加 zhihu 和 browser 配置模板 |
| `pyproject.toml` | 修改 | 添加 playwright 依赖 |
