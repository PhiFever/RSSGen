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
go run ./cmd/rssgen
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

## 后台刷新

RSSGen 会把本进程中真实访问过的 feed 动态加入后台刷新列表，避免只依赖 `config.yml` 手动维护预热列表。动态列表仅保存在内存中，重启后会重新学习；每个路由默认最多记录 200 个动态 feed，超过后按最久未访问淘汰。

```yaml
refresher:
  dynamic_feed_limit: 200 # 每个路由的动态 feed 上限；0 = 禁用动态学习
```

**查询参数：**

| 参数 | 说明 | 示例 |
|------|------|------|
| `format` | 输出格式，`atom`（默认）或 `rss` | `?format=rss` |
| `limit` | 返回条目数量，默认 20 | `?limit=10` |

## 故障通知

当后台刷新某个订阅源连续重试均失败、且为**业务错误**（默认 4xx：400/401/403/404/410/422/451）时，RSSGen 会：

1. 通过已配置的通知服务发送一条通知（当前支持飞书机器人）；
2. **禁用该订阅源**（feed 级，仅影响出错的那个 feed，同一路由下其他 feed 不受影响），后续 Miniflux 拉取该地址将返回 HTTP 502；
3. 重启 RSSGen 后自动恢复（禁用状态仅存于内存）。

临时错误（5xx、网络错误）只会重试，不触发通知或禁用。

**配置（config.yml）：**

```yaml
notifier:
  enabled: true            # 是否启用通知
  services:
    - type: feishu
      webhook_url: "https://open.feishu.cn/open-apis/bot/v2/hook/你的webhook_id"
      # secret: "签名密钥"  # 可选，飞书机器人签名验证密钥
  # business_error_codes:  # 可选，自定义业务错误状态码（默认 [400, 401, 403, 404, 410, 422, 451]）
  #   - 403
```

`/status` 会返回后台刷新器是否启用，以及按路由和 feed 分组的最近刷新状态。

## 致谢

感谢 [cv-cat/ZhihuApis: 知乎算法逆向](https://github.com/cv-cat/ZhihuApis) 关于知乎路由的启发
