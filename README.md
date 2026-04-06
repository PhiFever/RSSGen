# RSSGen

自托管 RSS 源生成框架，将任意网站转为标准 RSS/Atom 订阅源，通过 Docker Compose 与 Miniflux 阅读器集成部署。

## 快速开始

```bash
# 1. 复制配置文件并填入凭证
cp config.example.yml config.yml

# 2. Docker 一键部署（含 Miniflux + PostgreSQL）
docker compose up -d

# 3. 访问 Miniflux
# http://localhost:8080 (默认账号 admin / admin123)
# RSSGen 仅在 Docker 内部网络中可用，不对外暴露端口
```

本地开发：

```bash
uv sync
python main.py
```

## 支持的路由

### 爱发电 (Afdian)

订阅爱发电创作者的动态更新。

**订阅地址：** `http://localhost:8000/feed/afdian/{作者url_slug}`

其中 `{作者url_slug}` 是作者主页 URL 中的标识，例如作者主页为 `https://afdian.com/a/Alice`，则 slug 为 `Alice`。

**配置（config.yml）：**

```yaml
routes:
  afdian:
    enabled: true
    cookie: "你的爱发电 Cookie"
```

**获取 Cookie：**

推荐使用 [Cookie Master](https://chromewebstore.google.com/detail/cookie-master) 浏览器扩展：

1. 浏览器登录 [afdian.com](https://afdian.com)
2. 点击 Cookie Master 图标 → **Flat Copy**
3. 将复制的内容直接粘贴到 `config.yml` 中：

```yaml
routes:
  afdian:
    cookie: "_ga=GA1.1.xxx; auth_token=xxx; ..."
```

也可以通过开发者工具（F12）→ 网络标签页 → 任意请求的 `Cookie` 请求头中复制，格式相同。

**在 Miniflux 中使用：**

在 Miniflux 添加订阅时，填入 `http://rssgen:8000/feed/afdian/{作者url_slug}`（Docker 网络内使用容器名 `rssgen`）。

**注意：** 如果宿主机配置了 HTTP 代理（`HTTP_PROXY`/`HTTPS_PROXY`），Docker 容器可能会继承代理设置，导致 Miniflux 无法通过 Docker 内部域名访问 RSSGen（返回 502）。`docker-compose.yml` 中已通过 `NO_PROXY` 环境变量排除内部服务，如有自定义服务名请一并添加。

**查询参数：**

| 参数 | 说明 | 示例 |
|------|------|------|
| `format` | 输出格式，`atom`（默认）或 `rss` | `?format=rss` |
