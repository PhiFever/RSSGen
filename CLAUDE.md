# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 项目概述

RSSGen 是一个自托管 RSS 源生成框架（类似 RSSHub），使用 **Go** 编写。通过编写路由模块将网站（知乎、爱发电等）转为标准 RSS/Atom 订阅源，与 Miniflux 阅读器通过 Docker Compose 集成部署，目标平台含 ARM64（树莓派）。

> 项目曾为 Python/FastAPI 实现，已完全重写为 Go。任何提及 FastAPI、uv、curl_cffi、`RSSGen/` Python 包的历史文档均已过时。

## 技术栈

- **Go 1.25**，测试只用标准库 `testing` + `httptest`（无 testify）
- **HTTP 路由**: chi v5
- **反爬 HTTP 客户端**: bogdanfinn/tls-client（Chrome TLS/JA3 指纹模拟，见 `internal/scraper/`）
- **HTML**: bluemonday（消毒白名单）、golang.org/x/net/html（解析）
- **配置**: gopkg.in/yaml.v3
- **部署**: Dockerfile 多阶段构建 → scratch 镜像；Docker Compose（RSSGen + Miniflux + PostgreSQL）

## 常用命令

```bash
go build ./...                                        # 编译
go test ./...                                         # 全部测试
go test ./internal/route/zhihu/ -run TestName -v      # 单个测试
go vet ./...                                          # 静态检查
go run ./cmd/rssgen                                   # 本地运行（需当前目录有 config.yml）
docker compose up -d                                  # 部署（含 Miniflux + PostgreSQL）
```

发版：推送 `v*` tag 触发 GitHub Actions 构建 amd64/arm64 双架构镜像推送 GHCR。

## 架构

### 请求流程

```
Miniflux 定时拉取 → GET /feed/{route_name}/{params}
  → cmd/rssgen/main.go 的 makeFeedHandler
  → 解析 format/limit/include 并查 FeedCache（命中直接返回 XML）
  → 未命中：调用 pipeline.Refresh
      Route.Fetch → feed.Generate → 写缓存 → 返回
```

后台路径：`refresher` 按 config.yml 中各路由的 `refresh_interval` 定时刷新（启动预热、随机抖动、指数退避重试），实际抓取与 XML 生成同样委托给 `pipeline.Refresh`。

Feed XML 缓存键包含规范化后的输出变体：`route/path?format=atom&limit=20&include=a,b`。熔断/健康状态仍按基础 feed 键 `route/path` 管理。

### 模块地图（internal/）

- **pipeline/** — feed 生成流水线：复用 route 实例，规范化 `FetchOptions`，执行 `Route.Fetch`、`feed.Generate` 并写入 FeedCache
- **route/** — `Route` 接口、`FeedResult`/`FeedItem`/`FeedInfo` 数据结构、`HTTPError`（携带上游状态码供熔断判定）、路由注册表
- **route/zhihu/**、**route/afdian/** — 具体路由实现
- **backfill/** — 一次性历史回填深模块：feed 校验、全历史对账、重试、顺序与统计
- **miniflux/** — Miniflux HTTP 适配器：feed/entry 查询与 Entry Import
- **sign/zhihu/** — 知乎 x-zse-96 签名算法还原（打乱 base64 + SM4 变体 + XOR）。上游 JS 改版时最先失效的模块，版本常量集中在 `sign.go` 顶部
- **scraper/** — tls-client 封装：Chrome 指纹 profile、请求头及其顺序（风控敏感，头顺序必须手动维护）、Cookie、按实例限速、代理
- **feed/** — FeedItem → Atom/RSS XML，bluemonday 白名单消毒
- **refresher/** — 后台调度器，含 pending 去重、重试、熔断触发和错误状态统计（`/status` 端点数据源）
- **health/** — feed 健康状态管理：业务错误状态码判定与进程内 feed 禁用状态（重启恢复）
- **cache/** — 内存 TTL 缓存（存 feed XML）+ 基础 feed 键/变体缓存键构造
- **notifier/** — 业务错误通知发送（飞书 webhook）；不持有 feed 熔断状态
- **config/** — YAML 加载、默认值、`ResolveRoute` 合并全局 scraper 配置与路由级覆盖（同步/后台两条路径共用，保证行为一致）

### 新增路由的模式

1. 建包 `internal/route/{name}/`，实现 `route.Route` 接口（`Name`/`Fetch`），`Fetch` 返回 `route.FeedResult`
2. 在包的 `init()` 中 `route.Register("{name}", "路由描述", factory)`，factory 接收 `config.ResolvedRouteConfig` 强类型配置
3. 在 `cmd/rssgen/main.go` 匿名 import 该包
4. 上游返回非 2xx 时返回 `*route.HTTPError`，否则熔断/通知机制不生效
5. 测试注入用函数字段模式：路由结构体上放可替换的抓取函数字段（参考 afdian 的 `getPostListFn`、zhihu 的 `fetchActivitiesFn`），测试中替换为假实现

### 配置

- `config.yml.example` — 配置模板
- `config.yml` — 用户实际配置（已 gitignore），包含 Cookie 凭证、限速、代理、各路由的 feeds 列表

## 部署注意事项

- RSSGen 仅在 Docker 内部网络中提供服务，不对外暴露端口，Miniflux 通过容器名 `rssgen:8000` 访问
- 如宿主机配置了 HTTP 代理，需通过 `NO_PROXY` 环境变量排除 Docker 内部服务名（rssgen、db 等），否则 Miniflux 的 fetcher 会将内部请求转发到代理导致 502

## 注意事项

- 架构评审与待办清单（已知 bug、重构计划、执行顺序）见 `docs/architecture-review.md`，改动核心流程前先核对相关条目，完成后更新条目状态
- 所有文档和代码注释使用简体中文
- Dockerfile 需兼容 ARM64 架构
- 仓库根目录的 `*.json` 抓包样本、`*.sh` 脚本、二进制均为本地调试产物，已被 .gitignore 忽略，勿提交；测试夹具放 `internal/**/testdata/`（.gitignore 已豁免）
