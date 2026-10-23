---
name: backfill-config-file
description: backfill 的凭证与 Miniflux 地址改为全部从 config.yml 读取，移除 .env 与环境变量支持，并以 backfill.immich_url 取代 media.immich.public_url
type: project
status: ready-for-agent
date: 2026-10-23
---

# backfill 改为只读配置文件

## Problem Statement

`backfill` 的输入目前分散在三处：

- 环境变量或 `.env`：`MINIFLUX_URL`、`MINIFLUX_API_TOKEN`、`AFDIAN_COOKIE`、`ZHIHU_COOKIE`，由 `godotenv` 加载，进程环境变量优先；
- 命令行：`--miniflux-url` 覆盖 `MINIFLUX_URL`；
- v1.5.0 新增的 `--config`：只在 `execute`/`refresh` 上，只读取其中的 `media` 段，用于图片归档。

当初支持环境变量，是为了让不运行 RSSGen server 的人也能直接回填。但变量越来越多，配置起来很麻烦。另外，同一个 cookie 已经写在 `config.yml` 的 `routes.<source>.cookie` 里，用环境变量还得再填一遍。

图片归档还有第二个问题。在宿主机上运行 backfill 时，调用 Immich API 的地址（宿主机端口）和写进正文的地址（Miniflux 容器可访问的容器名）不一样，所以 v1.5.0 引入了 `media.immich.public_url`。这样一来，server 和 backfill 无法共用同一份 `media` 段：给 server 用的配置必须写 `url: immich_server`，给宿主机上的 backfill 用的配置必须写 `url: localhost` 再加上 `public_url`。

## Solution

backfill 的所有输入改为来自同一份 server 格式的配置文件（默认是当前目录的 `config.yml`），移除环境变量、`.env` 和 `--miniflux-url`。在配置文件中新增 `backfill:` 段，存放只有 backfill 才用的设置：Miniflux 的地址和 token，以及宿主机上访问 Immich API 的地址。

`media.immich.url` 恢复为单一含义：server 调用 API 的地址，同时也是写进正文的地址。backfill 需要用另一个地址调用 API 时，在 `backfill.immich_url` 中覆盖，写进正文的地址仍然是 `media.immich.url`。删除 `public_url`。

这样一份 `config.yml` 就能同时供 server 和宿主机上的 backfill 使用。只想回填、不运行 server 的人，只需写一个 `backfill` 段和对应路由的 cookie。

## User Stories

1. 作为只想回填、不运行 server 的使用者，我希望只写一份很短的配置文件就能使用 backfill，不必记住并设置一组环境变量。
2. 作为同时运行 server 的使用者，我希望 backfill 直接复用 `config.yml` 中已有的路由 cookie 和 `media` 段，不必重复填写。
3. 作为使用者，我希望在宿主机上回填时，只多写一个 `backfill.immich_url` 就能归档图片，而不必为 backfill 单独维护一份 `media` 段。
4. 作为使用者，我希望配置缺少必要字段时，报错能指明是哪个文件的哪个字段。

## Implementation Decisions

### 配置格式

```yaml
# 仅 backfill 使用；server 不读取此段
backfill:
  miniflux_url: "http://localhost:8080"        # 运行 backfill 的机器访问 Miniflux 的地址
  miniflux_api_token: "your-miniflux-api-token"
  # immich_url: "http://localhost:2283"         # 可选：backfill 调用 Immich API 的地址，正文仍写 media.immich.url

routes:
  zhihu:
    cookie: "..."   # backfill 只读取 cookie，不看 enabled 等其他字段
```

- `backfill` 段的字段都是平铺的，不嵌套子段。
- `media.immich.public_url` 被删除。`config.Load` 使用不严格的 `yaml.Unmarshal`，旧配置中残留的 `public_url` 会被静默忽略。不另写检测旧字段的代码，迁移方法写在 release note 中。
- `config.Load` 不校验 `backfill` 段：server 不需要它，缺失也不应影响 server 启动。`backfill` 段的校验全部放在 backfill 的执行路径中。

### 命令行

- `--config` 从 `execute`/`refresh` 移到 `backfill` 的公共参数（与 `--source`、`--request-interval` 同级，`list` 也能用），默认值为 `config.yml`。
- 删除 `--miniflux-url`。要切换 Miniflux 实例，就换一份配置文件。
- `--request-interval` 及其不得小于 1 秒的下限保持不变。回填数据源仍然不读取路由的 `rate_limit`、`proxy`、`impersonate`。

### 校验与报错

报错信息都要带上配置文件路径和字段的完整路径：

- 配置文件不存在或无法解析：沿用 `config.Load` 的报错。
- `backfill.miniflux_url` 或 `backfill.miniflux_api_token` 为空：`list`、`execute`、`refresh` 都报错。
- `routes.<source>.cookie` 为空：只有 `execute`/`refresh` 报错，`list` 不检查。不检查该路由的 `enabled`，因为 `enabled` 管的是 server 是否提供该路由。
- `backfill.immich_url` 非空但配置中没有 `media` 段：报错，因为写了这个字段说明使用者想归档图片，但归档实际上不会发生。
- `backfill.immich_url` 不是包含协议和主机的绝对 URL：报错，规则与 `media.immich.url` 相同。

### 图片归档

- 原来是"传了 `--config` 才归档，且必须有 `media` 段"，现在改为：**配置中有 `media` 段就归档，没有就不归档**，与 server 一致。删除"没有 `media` 段就报错"的逻辑。
- `backfill.immich_url` 非空时，backfill 调用 Immich API 用这个地址；写进正文的图片地址固定是 `{media.immich.url}/api/assets/{id}/original?key=...`。server 不受影响，它的 API 地址和正文地址都是 `media.immich.url`。
- 实现方式：`config.ImmichConfig` 删除 `PublicURL`，换成一个不参与 YAML 解析的 API 地址字段。这个字段为空时，`media` 包的 API 请求发往 `URL`；backfill 用 `backfill.immich_url` 填充它。
- 归档的其他规则（dry-run 不归档、Immich 侧失败时立即中止、结束时输出归档计数）不变。

### 删除项

- `github.com/joho/godotenv` 依赖、`loadBackfillEnv`，`commandDependencies` 中的 `loadBackfillEnv`/`getenv`，以及 `backfillSourceSpec.cookieEnv`。
- `.env.example`。
- `.gitignore` 和 `.dockerignore` 中的 `.env` **保留**：使用者本地可能还留着装有真实 cookie 的 `.env`。

### 文档

- README 的 backfill 各章节：删掉 `.env`、环境变量和 `--miniflux-url` 的说明，所有示例命令去掉 `--miniflux-url`。增加一份只给 backfill 用的最小 `config.yml` 示例，并说明完整配置见 `config.yml.example`。重写「回填时归档图片」一节，改用 `backfill.immich_url`。README 中"命令不读取 server 配置中的路由设置"一句，改为说明 backfill 只读取路由的 `cookie`。
- `config.yml.example`：增加被注释掉的 `backfill:` 段，删除 `public_url`。
- `2026-08-30-rss-image-archiving-design.md`：把正文中关于 `public_url` 和 `--config` 的描述改为本设计的结果，并追加一条修订记录。

## Testing Decisions

测试接缝沿用 `main_test.go` 中现有的做法：替换 `runBackfill` 后，断言 `backfillCLIConfig` 的内容。凭证改为写入临时目录中的配置文件，用 `--config` 指定，不再注入 `getenv`。

- 主路径：从配置文件读取 Miniflux 地址、token 和 `routes.<source>.cookie`。
- 不传 `--config` 时读取当前目录的 `config.yml`。
- `backfill.miniflux_url` 或 `miniflux_api_token` 缺失时报错。
- `execute` 缺少 cookie 时报错；`list` 缺少 cookie 时正常执行。
- `backfill.immich_url` 非空但没有 `media` 段时报错。
- 在 `media` 包中：设置了 API 地址覆盖时，API 请求发往覆盖地址，写进正文的地址仍是 `URL`。这条测试替换原来的 `public_url` 测试。
- 删除 `.env` 加载、`getenv` 回落、`--miniflux-url` 优先级，以及"`--config` 没有 `media` 段就报错"这几类测试。

## Out of Scope

- 回填数据源改为读取路由的 `rate_limit`、`proxy`、`impersonate`。
- 检测并提示旧配置中残留的 `public_url`，或者残留的 `.env`、环境变量。
- 让 server 读取或校验 `backfill` 段。

## Further Notes

- 不兼容变更：`--miniflux-url` 被删除，环境变量和 `.env` 不再读取，`media.immich.public_url` 被删除。计划发布为 `v1.6.0`：`public_url` 是在 v1.5.0 中与 backfill 归档同时引入的，唯一的用途就是在宿主机上运行 backfill；用 Docker 部署的 server 不受影响。
- 迁移方法：把 `.env` 中的 `MINIFLUX_URL`、`MINIFLUX_API_TOKEN` 分别写到 `backfill.miniflux_url`、`backfill.miniflux_api_token`，cookie 写到 `routes.<source>.cookie`；原来写在 `public_url` 的值改写到 `media.immich.url`，原来写在 `url` 的宿主机地址改写到 `backfill.immich_url`。
