# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 项目概述

RSSGen 是一个自托管 RSS 源生成框架（类似 RSSHub），使用 Python 编写。用户通过编写路由脚本将任意网站（爱发电、知乎、微信公众号等）转为标准 RSS/Atom 订阅源，通过 Docker Compose 与 Miniflux 阅读器集成部署。

## 技术栈

- **Python 3.12**，包管理使用 uv（见 pyproject.toml）
- **Web 框架**: FastAPI + uvicorn
- **HTTP 客户端**: curl_cffi（异步，自动模拟浏览器 TLS 指纹）
- **HTML 解析**: BeautifulSoup4 / parsel
- **Feed 生成**: feedgen
- **无头浏览器**: Playwright（用于需要 JS 渲染的场景如微信公众号）
- **配置**: PyYAML + Pydantic
- **缓存**: cachetools（内存） / Redis（可选）
- **部署**: Docker Compose（RSSGen + Miniflux + PostgreSQL）

## 常用命令

```bash
# 虚拟环境
uv sync                              # 安装依赖
source .venv/bin/activate            # 激活虚拟环境（Linux/macOS）

# 运行服务
uvicorn RSSGen.app:app --host 0.0.0.0 --port 8000 --reload

# Docker 部署（含 Miniflux + PostgreSQL）
docker compose up -d

# 测试
pytest
pytest tests/test_xxx.py::test_func  # 运行单个测试
```

## 架构

### 请求流程

```
Miniflux 定时拉取 → GET /feed/{route_name}/{params}
  → FastAPI 路由分发 → 匹配路由脚本
  → 检查缓存（命中直接返回 XML）
  → 调用 Route.fetch() 抓取数据源
  → feed.py 将 FeedItem 列表转为 Atom XML
  → 写入缓存，返回响应
```

### 核心模块（RSSGen/）

- **app.py** — FastAPI 主服务入口
- **config.py** — YAML 配置加载，Pydantic 校验
- **core/route.py** — `Route` 基类（定义 `feed_info()` 和 `fetch()` 接口）、`FeedItem`/`FeedInfo` 数据类、路由注册机制
- **core/feed.py** — feedgen 封装，FeedItem → Atom/RSS XML
- **core/scraper.py** — 基于 curl_cffi 的反爬封装（TLS 指纹模拟、Cookie 管理、频率控制、代理）
- **core/browser.py** — Playwright 无头浏览器封装
- **core/cache.py** — 双层缓存（内存 TTLCache / Redis）
- **routes/** — 路由脚本目录，自动发现继承 `Route` 的类并注册

### 路由编写模式

每个路由是 `routes/` 下的独立 Python 文件，继承 `Route` 基类，实现 `feed_info()` 和 `fetch()` 方法。`name` 属性决定 URL 前缀（`/feed/{name}/...`）。

### 配置

- `config.example.yml` — 配置模板
- `config.yml` — 用户实际配置（已 gitignore），包含凭证、风控参数等

## 部署注意事项

- RSSGen 仅在 Docker 内部网络中提供服务，不对外暴露端口，Miniflux 通过容器名 `rssgen:8000` 访问
- 如宿主机配置了 HTTP 代理，需通过 `NO_PROXY` 环境变量排除 Docker 内部服务名（rssgen、db 等），否则 Miniflux 的 fetcher 会将内部请求转发到代理导致 502

## 注意事项

- 所有文档和代码注释使用简体中文
- Dockerfile 需兼容 ARM64 架构
