# 架构评审与待办清单（2026-07）

评审对象：Go 重写后的 RSSGen（约 4400 行实现 + 5100 行测试，两条路由：zhihu、afdian）。

总体判断：模块划分清晰、测试覆盖充分、反爬细节到位。本项目不需要"更多架构"（不引入 DI 框架、消息队列、插件系统），需要的是把重复路径收敛成深模块。规模（两条路由、单机自托管）决定了简单就是对的。

各项均可独立派发实现，行号会随代码演进漂移，以函数名为准。完成后请更新对应条目状态。

---

## P0：文档与仓库卫生 ✅ 已完成（2026-07-07）

1. ~~CLAUDE.md 描述的是已废弃的 Python/FastAPI 架构~~ → 已重写为 Go 版现实
2. ~~`test.sh` 硬编码真实知乎凭证；根目录散落调试产物~~ → `.gitignore` 已加 `*.json`（豁免 `**/testdata/`）、`*.sh`、二进制
   - ⚠️ 遗留人工动作：`test.sh` 中的知乎 Cookie（含 `z_c0` 令牌）仍有效，需在知乎作废该会话并更换

## P1：正确性 bug ✅ 已完成（2026-07-07）

### 3. ~~缓存键不含请求变体，不同参数互相污染~~

已修复：`cache.BuildFeedCacheKey` / `pipeline.CacheKey` 将规范化后的 `format` / `limit` / `include` 并入 feed XML 缓存键；熔断健康键仍保留 `routeName + pathParams`，用于禁用同一个上游 feed 的所有输出变体。

回归测试：`TestFeedHandlerCacheVariantsDoNotPollute`、`TestRefreshGeneratesAndCachesVariant`、`TestBuildFeedCacheKeyIncludesVariant`。

### 4. ~~缓存未命中时对上游双重抓取~~

已修复：HTTP handler 未命中时只同步调用 `Pipeline.Refresh`，不再调用 `ref.Trigger()`；后台刷新入口保留在 refresher 的调度/Trigger 语义内。

回归测试：`TestFeedHandlerCacheMissDoesNotTriggerBackgroundFetch`。

## P2：架构深化 ✅ 已完成（2026-07-07）

### 5. ~~提取「feed 生成流水线」模块（最高价值的结构性改动）~~

已完成：新增 `internal/pipeline`，集中完成路由实例解析/复用、`FetchOptions` 规范化、`Route.Fetch`、`feed.Generate` 和写缓存。HTTP handler 只负责查缓存/调用 pipeline；refresher 只负责调度、pending 去重、重试、状态和熔断。

### 6. ~~`Route` 接口存在隐藏的调用顺序约束~~

已完成：`Route.Fetch` 现在返回 `route.FeedResult{Info, Items}`，接口已删除独立 `FeedInfo` 方法。知乎路由不再依赖跨调用的 `r.actor`，而是从本次抓取结果中提取 actor 生成 `FeedInfo`。

### 7. ~~Scraper 生命周期：每次 Fetch 新建，限速形同虚设~~

已完成：pipeline 按路由复用 `route.Route` 实例，zhihu/afdian 路由实例内部懒加载并复用同一个 `scraper.Scraper`，重试和并发刷新共享限速状态与 TLS 客户端。回归测试覆盖 pipeline 路由实例复用和两条路由的 scraper 复用。

## P3：可维护性（不紧急，逐项独立派发）

8. ~~**zhihu.go 类型化**：afdian 用 typed struct，zhihu 全程 `map[string]interface{}` + 类型断言（约 400 行）。1477 行测试打底，重构安全。建议定义 activity/target struct，变化大的字段用 `json.RawMessage`。~~ → 已完成（2026-07-07）：`internal/route/zhihu` 新增 activity/target/person/question/pin block 结构体，`target.content` 保留 `json.RawMessage` 以兼容字符串正文和 pin block 列表。
9. ~~**Notifier 职责拆分**：`disabledFeeds` 熔断状态与通知发送耦合（`internal/notifier/notifier.go`），HTTP handler 为路由决策依赖通知器。熔断属健康状态管理，建议拆出（并入 pipeline 或独立小组件）。~~ → 已完成（2026-07-07）：新增 `internal/health.FeedHealth` 管理业务错误码和 feed 禁用状态；`notifier.Notifier` 仅负责发送通知，HTTP handler/refresher 依赖健康组件做熔断判断。
10. **死配置清理**：`Server.CacheTTL` 从未被读取；`Cache.ArticleTTL` 只设默认值无人使用（均在 `internal/config/config.go`）。要么删除，要么实现——SQLite `articles` 表目前无限增长，树莓派上值得加基于 `fetched_at` 的保留策略，正好把 ArticleTTL 用起来。
11. ~~`.gitignore` 现代化~~ → 已随 P0 完成

---

## 建议执行顺序

| 批次 | 内容 | 验收标准 |
|------|------|----------|
| 1 ✅ | P0-1、P0-2（并行） | 已完成 |
| 2 ✅ | P2-5 流水线提取 → 修 P1-3、P1-4 → P2-6 接口合并 | `go test ./...` 全绿；已新增缓存变体与去重回归测试 |
| 3 ✅ | P2-7 Scraper 单例 | pipeline 复用 route 实例；zhihu/afdian 复用 scraper 实例 |
| 4 | P3-8/9/10（可选，独立派发） | 各自测试通过 |
