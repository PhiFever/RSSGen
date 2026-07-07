# 架构评审与待办清单（2026-07）

评审对象：Go 重写后的 RSSGen（约 4400 行实现 + 5100 行测试，两条路由：zhihu、afdian）。

总体判断：模块划分清晰、测试覆盖充分、反爬细节到位。本项目不需要"更多架构"（不引入 DI 框架、消息队列、插件系统），需要的是把重复路径收敛成深模块。规模（两条路由、单机自托管）决定了简单就是对的。

各项均可独立派发实现，行号会随代码演进漂移，以函数名为准。完成后请更新对应条目状态。

---

## P0：文档与仓库卫生 ✅ 已完成（2026-07-07）

1. ~~CLAUDE.md 描述的是已废弃的 Python/FastAPI 架构~~ → 已重写为 Go 版现实
2. ~~`test.sh` 硬编码真实知乎凭证；根目录散落调试产物~~ → `.gitignore` 已加 `*.json`（豁免 `**/testdata/`）、`*.sh`、二进制
   - ⚠️ 遗留人工动作：`test.sh` 中的知乎 Cookie（含 `z_c0` 令牌）仍有效，需在知乎作废该会话并更换

## P1：正确性 bug（待修，建议由 P2-5 的重构承载）

### 3. 缓存键不含请求变体，不同参数互相污染

缓存键只由 `routeName + pathParams` 构成（`cache.BuildCacheKey`，调用处 `cmd/rssgen/main.go` makeFeedHandler），不含 `format` / `limit` / `include`：

- 请求 `?format=rss` 未命中缓存时生成 RSS 写入缓存，后续 atom 请求命中后拿到 RSS
- 后台刷新固定生成 atom（`refresher.refreshOne`），反向覆盖
- 不同 `?include=` 过滤的变体同理互相覆盖

**方案**（二选一）：规范化变体并入缓存键；或砍掉查询参数自由度、只允许配置文件定义变体。倾向后者——更符合自托管场景，缓存语义干净。

### 4. 缓存未命中时对上游双重抓取

`makeFeedHandler` 未命中时先 `ref.Trigger()`（后台 goroutine 完整抓取），随后自己又同步抓取一遍。同一 feed 瞬间两轮上游请求，对知乎这类风控站点有封号风险。refresher 的 `pending` 去重只覆盖后台路径。

**方案**（二选一）：同步路径存在时不 Trigger；或彻底走读写分离语义（未命中返回空 feed + 后台填充，下次拉取命中）。后者与 refresher 的存在动机一致。

## P2：架构深化

### 5. 提取「feed 生成流水线」模块（最高价值的结构性改动）

`makeFeedHandler`（cmd/rssgen/main.go）和 `refresher.refreshOne` 各自完整实现了同一条流水线：查注册表 → `ResolveRoute` → 组装 `FetchOptions`（limit/include 解析都重复）→ `Fetch` → `FeedInfo` → `feed.Generate` → 写缓存。两条路径已出现行为漂移（format 处理不一致），是 bug 3、4 的温床。

**方案**：新建 `internal/pipeline`（或并入 route 包），接口一个方法：

```go
// 完成一次抓取到 XML 落缓存的全过程
func (p *Pipeline) Refresh(routeName string, pathParams []string, opts route.FetchOptions) (xml string, err error)
```

HTTP handler 退化为「查缓存 + 调用」，refresher 退化为「调度 + 重试 + 调用」。缓存键策略（bug 3）和去重（bug 4）在此处修一次全局生效。

### 6. `Route` 接口存在隐藏的调用顺序约束

zhihu 路由的 `FeedInfo` 依赖 `Fetch` 先执行填充 `r.actor`（`internal/route/zhihu/zhihu.go`），接口签名看不出此约束，调用顺序反了会静默降级（拿不到用户昵称/签名档）。

**方案**：合并为一次调用——`Fetch` 返回 `FeedResult{Info FeedInfo; Items []FeedItem}`，删除 `FeedInfo` 方法。建议与 P2-5 同批完成（动同一批文件）。

### 7. Scraper 生命周期：每次 Fetch 新建，限速形同虚设

每次 `Fetch` 都调 `scraper.New()`（zhihu/afdian 的 `getScraper`），限速状态 `lastRequestTime` 随新实例清零。Trigger 触发的刷新与定时刷新并发时，两个 Scraper 各自"限速"，对上游无全局节流；TLS 连接复用也丢失。对反爬项目这是核心语义缺失。

**方案**：Scraper 按「路由 × 配置」共享单例——工厂创建路由实例时创建一次并持有（已有 `SetCookies` 支持运行时更新）。注意 `refresher.refreshOne` 每次重试重建路由实例，需一并改为复用。

## P3：可维护性（不紧急，逐项独立派发）

8. **zhihu.go 类型化**：afdian 用 typed struct，zhihu 全程 `map[string]interface{}` + 类型断言（约 400 行）。1477 行测试打底，重构安全。建议定义 activity/target struct，变化大的字段用 `json.RawMessage`。
9. **Notifier 职责拆分**：`disabledFeeds` 熔断状态与通知发送耦合（`internal/notifier/notifier.go`），HTTP handler 为路由决策依赖通知器。熔断属健康状态管理，建议拆出（并入 pipeline 或独立小组件）。
10. **死配置清理**：`Server.CacheTTL` 从未被读取；`Cache.ArticleTTL` 只设默认值无人使用（均在 `internal/config/config.go`）。要么删除，要么实现——SQLite `articles` 表目前无限增长，树莓派上值得加基于 `fetched_at` 的保留策略，正好把 ArticleTTL 用起来。
11. ~~`.gitignore` 现代化~~ → 已随 P0 完成

---

## 建议执行顺序

| 批次 | 内容 | 验收标准 |
|------|------|----------|
| 1 ✅ | P0-1、P0-2（并行） | 已完成 |
| 2 | P2-5 流水线提取 → 修 P1-3、P1-4 → P2-6 接口合并（**一个 agent 串行**，动同一批文件，拆开派发会冲突） | `go test ./...` 全绿；新增缓存变体与去重回归测试；curl 验证 rss/atom 互不污染 |
| 3 | P2-7 Scraper 单例 | 并发触发两次刷新，日志确认请求间隔 ≥ rate_limit |
| 4 | P3-8/9/10（可选，独立派发） | 各自测试通过 |
