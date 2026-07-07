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

涉及：`cmd/rssgen/main.go`（makeFeedHandlerWithPipeline）、`internal/refresher/refresher.go`（Trigger、refreshOneWithOptions、splitCacheKey、GetStatus）、`internal/pipeline/pipeline.go`、`internal/cache/`

摩擦：「熔断/禁用用基础键 `route/path`、feed XML 用变体键 `route/path?format=…`」这条不变量没有单一归属——handler 与 refresher 各自手拼双键（3 包 5 处），refresher 还用 `splitCacheKey` 维护着 cache 键格式的手写逆函数；`errorStats` 以变体键为 map key，导致 `/status` 里 feed 名渗出 `user1?format=atom&limit=20`。任何一处把变体键误传给 `IsFeedDisabled`，禁用判定就静默错位。

方向：pipeline 单点构造 `FeedRef{HealthKey, CacheKey}`，调用方只传 routeName + pathParams + opts；`ErrorStatus` 改存 routeName / feedID / variant 结构化字段；删除 `splitCacheKey`。

验收：全仓无手拼键字符串；`/status` feed 名不含查询串；`go test ./...` 全绿。

## P2：架构深化

### 2. 提取路由基座：样板收成一份 【Strong】

涉及：`internal/route/zhihu/zhihu.go`（getScraper、Fetch 中 limit/include 兜底）、`internal/route/afdian/afdian.go`（getScraper）、`internal/route/route.go`、`internal/pipeline/pipeline.go`（normalizeOptions）

摩擦：`getScraper`（懒加载 + Cookie 刷新 + 复用）在两包逐字重复；`&route.HTTPError{...}` 构造出现 5 次；`limit<=0` 兜底和 zhihu 的 include 兜底重复了 pipeline `NormalizeFetchOptions` 已做过的规范化——zhihu 的 `getFeedInclude`/`DefaultInclude` 兜底分支经 pipeline 进来时近乎死代码，且与 pipeline 用不同的 config 快照推同一集合，存在悄悄发散的风险。加第三条路由要复制 40–60 行样板。

方向：route 包提供可嵌入的 baseRoute（scraper 生命周期 + HTTPError 帮助函数 + 样板方法）；include / limit 决策单点化在 pipeline，路由只消费最终 opts，删掉路由内兜底。可顺路带上条目 8。

验收：两包无重复 getScraper；include 优先级只在 pipeline 一处实现；新路由只需实现解析逻辑。

### 3. sign/zhihu 导出面 14 → 2 【Strong · 零风险】

涉及：`internal/sign/zhihu/sign.go`、`internal/sign/zhihu/sm4.go`

摩擦：包外唯一消费者是 `zhihu.go` 与 `cmd/zhihu_demo` 调 `GetSignature`；其余 13 个导出符号（ShuffledB64Encode/Decode、SM4 系列、XOR 常量等）把签名算法内部当接口泄漏。测试是同包测试（`package zhihu`），导出并非测试所需。

方向：除 `GetSignature` / `SignatureResult` 外全部改小写。纯机械改动，测试零迁移。

验收：包导出符号仅剩 2 个；`go build ./...` 通过。

### 4. health 深化：熔断编排收进 RecordFailure 【Worth exploring】

涉及：`internal/refresher/refresher.go`（refreshOneWithOptions 的错误分类段）、`internal/health/feed_health.go`、`internal/notifier/notifier.go`

摩擦：第一轮 P3-9 拆分后 health 退化成被动数据袋（IsBusinessError 判码 + DisableFeed 存标志），「提取状态码 → 判业务错误 → 通知 → 置禁用」四步编排留在 refresher。新增一种熔断触发条件要同时改两处——接缝切在了错的位置。

方向：health 提供 `RecordFailure(feedKey, err) (justDisabled bool)`，判码、置禁用、是否首次禁用收进一处；refresher 只上报错误并按返回值触发通知。建议在条目 1 之后做（摸同一批键）。

验收：熔断决策逻辑只在 health 包；测试直接打 health 接口，不需架起 refresher。

## P3：清理（逐项独立派发）

### 5. 死接口清理 + articles 保留策略（含原 P3-10）【Strong】

涉及：`internal/refresher/refresher.go`（Trigger、refreshOne）、`internal/pipeline/pipeline.go`（OptionsFromParams）、`internal/route/route.go`（FeedIDField）、`internal/cache/`（Delete、Len）、`internal/scraper/`（Post、PostJSON）、`internal/config/config.go`（Server.CacheTTL、Cache.ArticleTTL）、`internal/store/sqlite.go`、`cmd/rssgen/main.go`（makeRouter 首页 handler）

摩擦：

- 第一轮 P1-4 整改后 `refresher.Trigger` 生产端已死（`.Trigger(` 只剩测试调用），连带 `refreshOne`、`pipeline.OptionsFromParams` 只经它可达，且维护着与 `refreshOneWithOptions` 重复的 pending 去重
- `route.Route.FeedIDField()`、`cache.Delete/Len`、`scraper.Post/PostJSON` 零生产调用者
- `Server.CacheTTL` 零读取；`Cache.ArticleTTL` 只设默认值无人读；articles 表只 INSERT 不 DELETE，树莓派上无限增长
- 首页 handler 为读常量 `Description()` 每请求实例化全部路由工厂

方向：死符号按删除测试处理——删掉后复杂度不重现的一律删除；store 新增 `Prune(before time.Time)`（`DELETE FROM articles WHERE fetched_at < ?`），refresher 周期调用，把 ArticleTTL 变成真接口；注册表在 `Register` 时携带 description 元数据，首页不再实例化路由。

验收：全仓导出符号均有生产调用者（或明确的公共 API 理由）；articles 表有基于 fetched_at 的保留策略；配置文件每个字段都有对应行为。

### 6. 删除假想接缝：统一两条路由的测试注入 【Worth exploring】

涉及：`internal/route/afdian/afdian.go`（getAuthorIDFn、getPostListFn、getPostDetailFn、getPostCommentsFn 四个函数字段）、`internal/route/zhihu/zhihu.go`（fetchActivitiesFn）、`internal/route/afdian/afdian_test.go`（TestFetchWithMockServer）

摩擦：afdian 结构体里 4 个函数字段只有一个生产适配器（一个适配器 = 假想接缝），而它们替换的行为已经经 `hostURL` + httptest 这条真实接缝测过——同一批行为存在两套测试注入机制，测试脚手架渗进了生产结构体。zhihu 用另一套（fetchActivitiesFn），两条路由测法不一致。`TestFetchWithMockServer` 起了 httptest 却不覆盖 hostURL，实际对真实爱发电发请求且断言永不失败。

方向：统一 baseURL 可注入 + httptest；删 5 个函数字段，路由结构体回归纯数据；`TestFetchWithMockServer` 删除或改走 `setTestHostURL`。

验收：路由结构体无函数字段；`go test ./...` 不发任何真实外网请求。

### 7. afdian 详情抓取：删掉被限速锁串行化的伪并发 【Worth exploring】

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

## 建议执行顺序

| 批次 | 内容 | 验收标准 |
|------|------|----------|
| 1 | 条目 1 FeedRef 键收口 | `/status` feed 名无查询串；无手拼键；`go test ./...` 全绿 |
| 2 | 条目 5 死接口清理 + Prune（删 Trigger 链后键相关代码更少，接在条目 1 后最顺） | 死符号清零；articles 有保留策略 |
| 3 | 条目 2 路由基座（可含条目 8） | 新路由样板归零；include 单点 |
| 4 | 条目 3、4、6、7（独立派发，条目 3 随时可做） | 各自测试通过 |
