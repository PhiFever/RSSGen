# 架构评审与待办清单（2026-07 第二轮）

评审对象：第一轮整改完成后的 RSSGen（基线 commit dae47e3，约 4400 行实现 + 5100 行测试，两条路由：zhihu、afdian）。

总体判断：骨架健康——`pipeline` 是真正的收口点，`feed`（1 个导出符号扛 324 行实现）与 `sign/zhihu` 是现成的深模块。剩余摩擦集中在三处：**双键语义跨包泄漏**、**两条路由的样板重复**、**为测试而生的假想接缝混进生产结构体**。依旧不需要"更多架构"，需要的是收窄接口面、把散布的不变量收进单一归属。

各项均可独立派发实现，行号会随代码演进漂移，以函数名为准。完成后请更新对应条目状态。

---

## 第一轮（2026-07-07 已全部完成，详见 git 历史）

P0 文档与仓库卫生、P1 缓存变体键/双重抓取、P2 pipeline 提取/Route 接口合并/scraper 复用、P3-8 zhihu 类型化、P3-9 notifier/health 拆分均已完成。原 P3-10 死配置清理并入本轮条目 5。

- ⚠️ 遗留人工动作：`test.sh` 中的知乎 Cookie（含 `z_c0` 令牌）仍有效，需在知乎作废该会话并更换

---

## P1：正确性风险

### 1. 双键语义收口：pipeline 暴露 FeedRef 【Strong】

状态：已完成（2026-07-08）。

涉及：根目录 `main.go`（makeFeedHandlerWithPipeline）、`internal/refresher/refresher.go`（Trigger、refreshOneWithOptions、splitCacheKey、GetStatus）、`internal/pipeline/pipeline.go`、`internal/cache/`

摩擦：「熔断/禁用用基础键 `route/path`、feed XML 用变体键 `route/path?format=…`」这条不变量没有单一归属——handler 与 refresher 各自手拼双键（3 包 5 处），refresher 还用 `splitCacheKey` 维护着 cache 键格式的手写逆函数；`errorStats` 以变体键为 map key，导致 `/status` 里 feed 名渗出 `user1?format=atom&limit=20`。任何一处把变体键误传给 `IsFeedDisabled`，禁用判定就静默错位。

方向：pipeline 单点构造 `FeedRef{HealthKey, CacheKey}`，调用方只传 routeName + pathParams + opts；`ErrorStatus` 改存 routeName / feedID / variant 结构化字段；删除 `splitCacheKey`。

验收：全仓无手拼键字符串；`/status` feed 名不含查询串；`go test ./...` 全绿。

## P2：架构深化

### 2. 提取路由基座：样板收成一份 【Strong】

状态：已完成（2026-07-08）。

涉及：`internal/route/zhihu/zhihu.go`（getScraper、Fetch 中 limit/include 兜底）、`internal/route/afdian/afdian.go`（getScraper）、`internal/route/route.go`、`internal/pipeline/pipeline.go`（normalizeOptions）

摩擦：`getScraper`（懒加载 + Cookie 刷新 + 复用）在两包逐字重复；`&route.HTTPError{...}` 构造出现 5 次；`limit<=0` 兜底和 zhihu 的 include 兜底重复了 pipeline `NormalizeFetchOptions` 已做过的规范化——zhihu 的 `getFeedInclude`/`DefaultInclude` 兜底分支经 pipeline 进来时近乎死代码，且与 pipeline 用不同的 config 快照推同一集合，存在悄悄发散的风险。加第三条路由要复制 40–60 行样板。

方向：route 包提供可嵌入的 baseRoute（scraper 生命周期 + HTTPError 帮助函数 + 样板方法）；include / limit 决策单点化在 pipeline，路由只消费最终 opts，删掉路由内兜底。可顺路带上条目 8。

验收：两包无重复 getScraper；include 优先级只在 pipeline 一处实现；新路由只需实现解析逻辑。

### 3. sign/zhihu 导出面 14 → 2 【Strong · 零风险】

状态：已完成（2026-07-08）。

涉及：`internal/sign/zhihu/sign.go`、`internal/sign/zhihu/sm4.go`

摩擦：包外唯一消费者是 `zhihu.go` 与 `cmd/zhihu_demo` 调 `GetSignature`；其余 13 个导出符号（ShuffledB64Encode/Decode、SM4 系列、XOR 常量等）把签名算法内部当接口泄漏。测试是同包测试（`package zhihu`），导出并非测试所需。

方向：除 `GetSignature` / `SignatureResult` 外全部改小写。纯机械改动，测试零迁移。

验收：包导出符号仅剩 2 个；`go build ./...` 通过。

### 4. health 深化：熔断编排收进 RecordFailure 【Worth exploring】

状态：已完成（2026-07-08）；2026-11-15 再收一步——同步 handler 也长出了同一段「按返回值通知」的编排，两处重复，于是改为 health 持有 `OnDisabled` 回调，`RecordFailure` 不再有返回值，首次禁用的日志与通知都在 health 内触发，refresher 不再依赖 notifier。下文「按返回值触发通知」的方向描述已被取代。

涉及：`internal/refresher/refresher.go`（refreshOneWithOptions 的错误分类段）、`internal/health/feed_health.go`、`internal/notifier/notifier.go`

摩擦：第一轮 P3-9 拆分后 health 退化成被动数据袋（IsBusinessError 判码 + DisableFeed 存标志），「提取状态码 → 判业务错误 → 通知 → 置禁用」四步编排留在 refresher。新增一种熔断触发条件要同时改两处——接缝切在了错的位置。

方向：health 提供 `RecordFailure(feedKey, err) (justDisabled bool)`，判码、置禁用、是否首次禁用收进一处；refresher 只上报错误并按返回值触发通知。建议在条目 1 之后做（摸同一批键）。

验收：熔断决策逻辑只在 health 包；测试直接打 health 接口，不需架起 refresher。

## P3：清理（逐项独立派发）

### 5. 死接口清理（含原 P3-10）【Strong】

状态：已完成（2026-07-08；持久化相关预留在 2026-08-26 的 Afdian 历史回填阶段移除）。`deadcode ./...` 已无输出。

涉及：`internal/refresher/refresher.go`（Trigger、refreshOne）、`internal/pipeline/pipeline.go`（OptionsFromParams）、`internal/route/route.go`（FeedIDField、Unregister）、`internal/cache/`（Delete、Len）、`internal/scraper/`（Post、PostJSON）、`internal/config/config.go`（Server.CacheTTL）、根目录 `main.go`（makeRouter 首页 handler、makeFeedHandler）

摩擦：

- 第一轮 P1-4 整改后 `refresher.Trigger` 生产端已死（`.Trigger(` 只剩测试调用），连带 `refreshOne`、`pipeline.OptionsFromParams` 只经它可达，且维护着与 `refreshOneWithOptions` 重复的 pending 去重
- `route.Route.FeedIDField()`、`route.Unregister`、`cache.Delete/Len`、`scraper.Post/PostJSON` 零生产调用者
- `Server.CacheTTL` 是旧位置死配置
- 首页 handler 为读常量 `Description()` 每请求实例化全部路由工厂

方向：死符号按删除测试处理——删掉后复杂度不重现的一律删除；注册表在 `Register` 时携带 description 元数据，首页不再实例化路由；`Server.CacheTTL` 从配置和示例中删除。

验收：本轮覆盖的死符号清零；首页不再实例化路由；配置文件字段均有对应行为。

### 6. 删除假想接缝：统一两条路由的测试注入 【Worth exploring】

状态：已完成（2026-09-09）。5 个函数字段与 `fetchActivitiesFunc` 类型已删除，两条路由的 27 个 Fetch 测试统一改走 `withTestHost` / `withZhihuTestHost` + httptest。

涉及：`internal/route/afdian/afdian.go`（getAuthorIDFn、getPostListFn、getPostDetailFn、getPostCommentsFn 四个函数字段）、`internal/route/zhihu/zhihu.go`（fetchActivitiesFn）、`internal/route/afdian/afdian_test.go`（TestFetchWithMockServer）

摩擦：afdian 结构体里 4 个函数字段只有一个生产适配器（一个适配器 = 假想接缝），而它们替换的行为已经经 `hostURL` + httptest 这条真实接缝测过——同一批行为存在两套测试注入机制，测试脚手架渗进了生产结构体。zhihu 用另一套（fetchActivitiesFn），两条路由测法不一致。（原「`TestFetchWithMockServer` 起了 httptest 却不覆盖 hostURL，实际对真实爱发电发请求且断言永不失败」已修复。）

方向：统一 baseURL 可注入 + httptest；删 5 个函数字段，路由结构体回归纯数据。

验收：路由结构体无函数字段。

改后遗留的两处断言语义变化（因旧假接缝与真实实现行为不一致）：

- `TestFetchStopsWhenLimitReached`：旧 mock 自行按 limit 截断，真实实现里 limit 只约束翻页循环、不截断单页条目。改为每页 1 条 + `paging.next` 驱动，断言 limit=2 时只请求 2 页。
- `TestFetchPropagatesError`：改用「200 + 非法 JSON」触发解析失败，与仍断言 403 的 `TestFetchRaisesOnNon200` 区分（5xx 会触发 `fetchActivitiesPaged` 的重试 sleep，错误路径一律避开 5xx）。

### 7. afdian 详情抓取：删掉被限速锁串行化的伪并发 【Worth exploring】

状态：已完成（2026-11-15）。改为顺序抓取，同一时刻至多一个在途请求；断言并发的 `TestFetchDetailsRunConcurrentlyAndPreserveOrder` 一并删除，顺序由 `TestFetchPipelineOrderPreserved` 覆盖。复核时发现下文「性能等价」只在单次请求耗时不超过限速间隔时成立（`rateLimitWait` 的锁只罩住等待，请求在锁外）：默认 1s 限速下等价，上游单次响应慢于限速间隔时顺序版更慢——这是为了让限速语义名副其实而接受的代价。

涉及：`internal/route/afdian/afdian.go`（Fetch 中 missing 详情抓取段）、`internal/scraper/scraper.go`（rateLimitWait）

摩擦：每条缺失详情各起一个 goroutine，但共用同一个 Scraper，`rateLimitWait` 用互斥锁 + Sleep 把所有请求串行化——并发拿不到任何吞吐（限速语义是 scraper 接口的一部分），却付出 WaitGroup + contents/contentOK 共享切片按下标并发写的复杂度。

方向：改顺序抓取，性能等价，删掉并发脚手架。（若未来真要并发，应改 scraper 限速为令牌桶——那是另一个决定，勿两头不到岸。）

验收：无 WaitGroup / 共享切片；现有测试全绿。

## 搁置

### 8. scraper 硬编码知乎专属头上浮为配置 【Speculative】

涉及：`internal/scraper/scraper.go`（chromeHeaderOrder 中的 x-zse-93、x-zse-96）

摩擦：被两条路由共享的底层 scraper 的 header 顺序表里硬写了知乎签名专属头——路由专属细节跨接缝反向泄漏进通用模块，afdian 永远不发这两个头。

搁置理由：现状两条路由都正确工作，header 顺序风控敏感需手动维护，价值在第三条路由或风控排查时才兑现。若做条目 2 的路由基座，可顺路带上（header order 作为 scraper.Config 字段传入或提供合并钩子）。

---

## 第三轮（2026-11-15）：小项清理与结构收口

全部完成（`deadcode ./...` 无输出，`go test ./...` 全绿）。

小项清理：

- refresher：删除只剩测试调用的 `refreshOneWithOptions` 包装，`refreshOneWithOptionsSource` 更名 `refreshOne`；删除只写不读的 `pending` 表
- 调度判定单点化：`config.RouteConfig.NeedsScheduling` 取代 `main.go` 与 refresher 各写一份的同一条件
- 死符号：`zhihu.ParseStartBefore`、`route.FetchOptions.ExtraParams`、scraper `doRequest` 的 `method` / `body` 参数（并入 `Get`）
- 手写 `contains` / `containsCategory` 换成标准库 `slices.Contains`
- afdian：`Fetch` 与 `feedInfo` 重复的空路径校验、翻页里重复的「空页收尾」分支各合成一处
- `makeFeedHandlerWithPipeline` 更名 `makeFeedHandler`（被区分的旧函数早已删除）
- 测试：`main_test.go`、`refresher_test.go` 中与 `cache_test.go` 重复的 `TestBuildCacheKey` 删除
- 注释与文档：feedcatalog / pipeline / refresher 的英文注释改中文；AGENTS.md、CODING_STANDARDS.md 中「函数字段注入」改为现行的 httptest 接缝

结构收口（各项均推翻或补完了此前的取舍）：

- **条目 7 afdian 顺序抓取**：见条目 7 状态
- **熔断通知编排收进 health**：见条目 4 状态。`makeFeedHandler` 与 `refreshOne` 各只剩一行 `RecordFailure`；`main.go` 的 `feedNotifier` 接口、`refresher.Config.Notifier` 删除
- **`/status` 按变体列出**：`GetStatus` 返回 `路由 → feedID → []ErrorStatus`，按缓存键升序；此前同一 feed 多变体时留下哪一条取决于 map 遍历顺序。**JSON 形态变化**：feed 的值由对象变为数组
- **进程内状态清理**：refresher 每轮刷新前删掉该路由下已不在刷新目标里的状态（被 feedcatalog 淘汰的动态变体）；`TTLCache` 在 `Set` 时清扫过期条目，删除一律用 `CompareAndDelete`
- **删除生产代码里的测试兜底**：`refresher.New` 不再自建 pipeline / health（`Config.FeedCache`、`Config.ScraperConfig` 随之删除），`makeFeedHandler` 不再兜底 `feedHealth`，`makeRouter` 从未使用的 `cfg` 参数删除；测试改用 `newTestRefresher` 辅助构造
- **`Route.Name()` 删除**：路由名只由 `route.Register` 的键承载，`Route` 收成单方法接口，`NewBaseRoute` 随之删除，路由直接嵌入零值 `route.BaseRoute`
- **缓存键构造归位 pipeline**：`internal/cache/cachekey.go` 删除，基础键与变体键只在 pipeline 构造，cache 包只剩 TTL 缓存；键格式未变
- **scraper 无生产者符号**：`Config.ExtraHeaders`、`Response.Text` 删除。enclosure 序列化不在此列——那是明确保留的 feed 通用能力

后续四项已完成（2026-11-15）：

- 入口按职责拆成 `main.go`（根命令与信号）、`server.go`（服务装配与生命周期）、`handlers.go`（HTTP 路由与响应）、`backfill_cli.go`（回填命令、参数与输出），保持同一个 main 包
- `--request-interval` 的下限与校验规则归属 `backfill.MinRequestInterval` / `ValidateRequestInterval`；CLI Validator 和数据源构造器复用，删除参数转换中的重复校验，保留直接调用构造器时的约束
- `startup_delay` / `max_retries` / `retry_base_delay` 默认值统一为 config 包常量，配置加载与刷新器构造共用；原有零值及非正值回退行为不变
- 删除 `Refresher.jitterDurationFn` / `sleepFn`；测试通过标准库 `testing/synctest` 验证真实计时器的批次抖动、预热无抖动以及等待取消

---

## 建议执行顺序

| 批次 | 内容 | 验收标准 |
|------|------|----------|
| 1 | 条目 1 FeedRef 键收口 | `/status` feed 名无查询串；无手拼键；`go test ./...` 全绿 |
| 2 | 条目 5 死接口清理（删 Trigger 链后键相关代码更少，接在条目 1 后最顺） | `deadcode ./...` 无输出；首页不再实例化路由 |
| 3 | 条目 2 路由基座（可含条目 8） | 新路由样板归零；include 单点 |
| 4 | 条目 3、4、6、7（独立派发，条目 3 随时可做） | 各自测试通过 |
