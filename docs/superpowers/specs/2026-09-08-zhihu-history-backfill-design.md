---
name: zhihu-history-backfill
description: 将 backfill CLI 统一为 backfill --source，扩展 backfill 深模块以容纳「正文随列表返回」与「按 category 过滤」，并新增知乎全历史回填适配器
type: project
status: draft
date: 2026-09-08
related: 2026-08-26-afdian-history-backfill-design.md
---

# 知乎历史回填与 backfill 命令统一

## Problem Statement

Afdian 历史回填已经落地：`internal/backfill` 是一个深模块，通过 `Source` / `Destination` 两个窄接缝把上游抓取与 Miniflux 对账隔开，CLI 以 `afdian-backfill` 前台子命令驱动。知乎路由目前只能返回最新若干条动态，用户在 Miniflux 里的知乎订阅无法补齐历史。

但「仿照 afdian 再写一个」不成立，因为两条路由的抓取形状根本不同：

- **Afdian 是三段式**：`get-post-list` 只返回 `post_id` / `title` / `publish_time`，正文必须再打 `get-detail`，评论再打第三个接口。`Source` 接口的 `Discover` / `Detail` / `Comments` 正是照这个形状裁的，三个方法对应三种真实 HTTP 请求。
- **知乎是一段式**：`/api/v3/moments/{id}/activities` 一次响应里，每个 activity 的 `target.content` 就是完整正文 HTML（`zhihu.go` 的 `renderContent` 直接从中取）。翻完页正文已经全部在手。同时现有 RSS 路径从不抓取评论，知乎侧没有 `Comments` 的对应上游请求。

知乎还有 Afdian 完全没有的 **category 维度**：动态分为 answer / article / pin / collected_\* / voteup_\* / followed_question 九类，server 端通过路由级 `default_include` 与 feed 级 `include` 过滤。而 backfill 按既有决策不读 server 配置，看不见这份过滤。若忽略它，一个原本只订阅 answer/article/pin 的 feed 会被灌进几百条「赞同了回答」。

CLI 侧的问题是重复：现有 `newAfdianBackfillCommand` / `afdianBackfillCLIConfig` / `afdianBackfillConfigFromCommand` / `runAfdianBackfill` / `printBackfillResult` 是一整套单数据源样板，复制一份给知乎会新引入约 100 行重复。

## Solution

本阶段分两步交付，重构先于新代码。

**阶段一 · CLI 统一（不改 `internal/backfill` 接口）。** 把 `afdian-backfill` 重构为 `backfill --source {afdian,zhihu}` 统一子命令，flag 定义、配置解析、结果打印按（数据源名、cookie 环境变量名、Source 构造器）参数化。此阶段唯一的数据源仍是 Afdian，用现有全链路验证行为不变，`go test ./...` 全绿且 `backfill --source afdian --list-feeds` 与旧命令输出逐字一致即为完成。

**阶段二 · 接口扩展与知乎适配器。** 扩展 `internal/backfill` 以容纳两个知乎特性——`Candidate.Content` 让正文随列表返回，`Candidate.Category` / `Request.IncludeCategories` / `Source.KnownCategories` 让类别过滤成为共享模块的一等概念——随后落地知乎适配器。新字段与它们的第一个消费者同时出生，避免产出只能写空转测试的死字段。

历史来源沿用现有的 `/api/v3/moments/{id}/activities`，拆掉 `limit` 截断后深翻到上游自然结束。不引入 `/api/v4/members/{id}/answers`、`/articles` 或 `api.zhihu.com/pins/{id}/moments`：那三个端点确实存在且 `include` 里能带 `content`（RSSHub 的 `lib/routes/zhihu/{answers,posts,pin/people}.ts` 用的就是它们），但 RSSHub 的每个 handler 都只取第一页就返回，并未证明其 `offset` 分页能推到全历史；而改用它们要付出三套新解析的代价，还会彻底丢失 collected / voteup / followed_question 五类动态。动态流能翻多深是一个可测量的事实，`--dry-run` 正是为回答它而存在——先用现有端点实现，用实测数据决定是否需要第二来源。

## User Stories

1. 作为操作者，我希望回填命令统一为 `backfill --source`，这样只需要记住一套参数。
2. 作为操作者，我希望 `--source` 是必填项，这样命令永远不会对我没指定的数据源动手。
3. 作为操作者，我希望旧的 `afdian-backfill` 直接消失而非留作别名，这样文档和实际行为不会分叉。
4. 作为操作者，我希望两个数据源各用各的 cookie 环境变量，这样同一份 `.env` 能同时供两者使用。
5. 作为操作者，我希望把仅适用于某个数据源的选项传给另一个数据源时命令直接报错，这样过滤失效不会变成需要读代码才能发现的坑。
6. 作为操作者，我希望知乎回填扫描动态流直到上游自然结束，这样没有人为水位截断我的历史。
7. 作为操作者，我希望翻页遇到重复游标时终止，这样上游分页异常不会变成无限请求。
8. 作为操作者，我希望回填不设页数上限，这样安全阀不会与「补齐全历史」的目的冲突。

   > **修订（2026-09-08，实测后）**：本条被实测推翻。见下文「不设页数上限」的修订。
9. 作为操作者，我希望知乎正文随列表一次取回，这样回填不会为已经拿到的内容重复请求。
10. 作为操作者，我希望回填生成的标题、链接、正文与 RSS 路径逐字一致，这样两条摄入路径的去重能够收敛。
11. 作为操作者，我希望回填按 feed URL 上的 `include` 过滤类别，这样补进来的东西不会超出这个订阅原本的范围。
12. 作为操作者，我希望 `--include` 能显式覆盖 feed URL 的类别，这样 server 端配置驱动的、backfill 看不见的过滤仍然可控。
13. 作为操作者，我希望 `--include` 与 feed URL 上的类别都经过白名单校验，这样我对这个 feed 的理解有误时会立刻收到报错。
14. 作为操作者，我希望类别校验发生在翻页之前，这样拼错参数不会在翻完整段历史之后才暴露。
15. 作为操作者，我希望校验错误不影响 `--list-feeds`，这样一个 feed 不会因为 URL 上的历史遗留参数而从列表里静默消失。
16. 作为操作者，我希望解析器不认识的动态类型被跳过并计数，这样订阅里不会多出一批标题为「未知内容」、链接靠猜的永久条目。
17. 作为操作者，我希望跳过数量出现在 dry-run 结果里，这样我在执行前就知道解析器是否已经落后于上游。
18. 作为操作者，我希望默认过滤掉作者对自己内容的互动，这样回填与 RSS 路径的默认行为一致。
19. 作为操作者，我希望 dry-run 按类别分组报告缺失数量，这样执行前能看清会往订阅里塞什么。
20. 作为操作者，我希望知乎 cookie 缺少 `d_c0` 时在命令启动阶段就报错，这样不必等到第一次请求失败。
21. 作为操作者，我希望知乎请求串行且默认至少间隔一秒、可调大不可调小，这样深翻页不会比预期更激进。
22. 作为操作者，我希望翻页进度被持续汇报，这样长时间运行的前台任务是可观察的。
23. 作为操作者，我希望已存在的条目不触发任何额外请求，这样重跑代价接近于零。
24. 作为维护者，我希望重构先于新功能完成并通过测试，这样接口变更的影响面可以被单独验证。
25. 作为维护者，我希望类别过滤与统计位于共享模块而非知乎适配器，这样第三个数据源能直接复用同一套语义。
26. 作为维护者，我希望共享模块通过一个接口方法向数据源询问合法类别，这样校验与未知类型跳过复用同一机制。
27. 作为维护者，我希望没有 category 概念的数据源在这套机制下完全不受影响，这样 Afdian 行为逐字不变。
28. 作为维护者，我希望知乎回填适配器用 `httptest` 覆盖主机地址来测试，这样不会在已判定要清理的假想接缝上再加一处。
29. 作为维护者，我希望测试不发起真实的知乎或 Miniflux 请求，这样测试套件保持确定且无需凭证。

## Implementation Decisions

### CLI

- 单一回填子命令 `rssgen backfill`，通过必填 `--source {afdian,zhihu}` 选择数据源。`afdian-backfill` 直接改名，**不保留兼容别名**：它是一次性前台工具而非被脚本长期依赖的服务接口，破坏成本接近零，保留别名会让「只有一套命令」的重构失去意义。`README.md` 中三处调用示例同步更新。

- 位置参数继续被 `rejectPositionalArgs` 拒绝，数据源通过 flag 而非位置参数或二级子命令表达。

- 通用 flag：`--source`（必填）、`--miniflux-url`（必填）、`--feed-id`、`--list-feeds`、`--dry-run`、`--request-interval`。数据源专属 flag：`--include`、`--include-self-interaction`（均仅适用于 zhihu）。

- 把专属 flag 传给不适用的数据源时**报错退出**，而不是静默忽略。静默忽略会让「我明明加了过滤却没生效」变成需要读代码才能发现的问题。

- Cookie 按数据源各用各的环境变量：`AFDIAN_COOKIE` 与 `ZHIHU_COOKIE`，不合并为单一变量。两者可以同时存在于一份 `.env`，合并则要求每次换源都改环境变量。`MINIFLUX_API_TOKEN` 保持不变。密钥继续只从环境变量读取，不进入进程参数，不写入日志。

- `--request-interval` 默认 1s、下限 1s，两个数据源一致。

- 命令层不学习分页、去重、导入排序规则；这些留在 `internal/backfill` 内。CLI 只负责解析参数、构造对应数据源的 `Source`、调用 `backfill.Run`、打印结果。

### backfill 模块接口扩展（阶段二）

- `Source.FeedIdentity` 的返回值从 `(string, error)` 改为 `(Identity, error)`，其中 `Identity{SourceID string; Categories []string}`。共享 helper `FeedIdentityFromURL` 在校验路径形状的同时解析 query 中的 `include`——该参数名是 RSSGen feed URL 的通用约定（`pipeline.OptionsFromQuery` 读的正是它），不是知乎私有语义。

- **Afdian 的 `FeedIdentity` 必须显式丢弃解析出的 categories**，只返回 `Identity{SourceID: slug}`。Afdian 候选的 `Category` 恒为空串，一旦某个 Afdian feed 的 URL 上恰好挂着 `?include=xxx`，`Run` 会拿它过滤全部候选并静默得到零导入。Afdian 路由本来就不解释 `include`，丢弃是正确语义。

- `Candidate` 新增两个字段：`Content string` 与 `Category string`。`Run` 中详情获取改为「`Content` 非空则直接使用，为空才调用 `Source.Detail`」。这让「正文随列表返回」成为接口的诚实表达，而不是让适配器在内部缓存里假装自己在取详情。

- `Request` 新增 `IncludeCategories []string`。最终生效的类别集合由 `Run` 按一条规则决定：`Request.IncludeCategories` 非空则用它，否则用 `Identity.Categories`；两者皆空则不过滤。

- `Result` 新增 `MissingByCategory map[string]int` 与 `SkippedUnknown int`。不新增 `ScannedByCategory`：类别过滤发生在对账之前，过滤后 scanned 的键集与 missing 的键集完全相同，第二个 map 提供不了新信息。

  > **修订（2026-09-08，实测后）**：`SkippedUnknown` 已删除。未知类别改为放行（见下文「未知类别」条的修订），该计数恒为 0，属于本次改动造成的未使用项。

- `Source` 接口新增 `KnownCategories() []string`，返回该数据源的合法类别白名单；返回 `nil` 表示本数据源没有 category 概念。Afdian 返回 `nil`，与类别相关的全部逻辑对它整段跳过。这与之前拒绝为评论能力加接口方法的判断不冲突：`IncludeCategories` 已经是共享模块持有的概念，共享模块持有它却不知道取值域，向数据源问一次是职责划分而非能力协商。

- `Run` 在 execute / dry-run 分支的顺序固定为：解析目标 feed 与 identity → 确定最终类别集合 → **校验类别** → 读取 Miniflux 现有条目 → 发现上游历史 → 按类别过滤 → 对账 →（dry-run 到此为止）→ 逐条导入。校验排在翻页之前，拼错的 `--include` 不会在翻完整段历史之后才暴露。

- 类别校验对**两条入口同等严格**：`--include` 与 feed URL 上的 `include` 有任一值不在 `KnownCategories()` 中即报错退出。`ActionList` 分支不做类别校验——`FeedIdentity` 在那里只用于判断 feed 是否属于当前数据源，让校验错误参与筛选会导致带非法 include 的 feed 从 `--list-feeds` 输出中静默消失，与「显式报错」的要求正相反。

- 上游候选中 `Category` 不在 `KnownCategories()` 内的条目被**跳过**，计入 `SkippedUnknown` 并记录 `Warning`，不计入 `Scanned`。理由是回填与 RSS 路径的风险不对称：未识别类型在 `makeFeedItem` 中落到 `default` 分支，标题兜底为字面量「未知内容」，链接是拼出来的 `https://www.zhihu.com/{target_type}/{id}`；在 RSS 路径上这个降级结果会随缓存过期滚掉，在回填路径上它会成为 Miniflux 永久条目，而那个猜测出来的 URL 同时是 `reconcile` 的去重判据——一旦写错，将来即使代码支持了该类型也再匹配不上。跳过而非中断整次运行：知乎历史中出现一条早已下线的动态类型，不该让整份历史回填不了。

  > **修订（2026-09-08，实测后）**：改为**放行**，按用户决定。跳过的做法让两条路径分叉：RSS 路径会把未识别类型照常发布给 Miniflux（`zhihu.go:410-418` 的 `default` 分支产出 item，category 兜底为 `target.Type`），回填却把它丢弃，与本设计反复强调的「两条路径逐字一致」冲突。实测 feed 87（`Chiaki_Hinata`）出现一条 `MEMBER_FOLLOW_FAVORITES` / `target_type=collection`（「关注了收藏夹」），被旧逻辑跳过。
  >
  > **遗留问题（已知，待后续处理）**：上面那段关于「猜测 URL 成为永久去重判据」的担忧**依然成立**，放行并没有解决它，只是接受了它。`https://www.zhihu.com/{target_type}/{id}` 猜对与否取决于类型——实测这条 `collection/26815754` 恰好是知乎收藏夹的真实 URL 格式，但没有任何机制保证下一种未知类型也这么走运。将来若为某个类型补上正式支持，其链接形态一旦与当年猜测的不同，`reconcile` 就匹配不上，会产生重复条目。用户已知悉并计划后续处理。
  >
  > 显式 `--include` 仍然排除未知类别：用户点名了要哪些类别，未知类别不在其中。`KnownCategories()` 保留，用途收窄为校验 `--include` 的取值。
  >
  > **修订（2026-09-08，实机抓包后）**：`collection` 已不再是未知类型。实测该动态为 `verb=MEMBER_FOLLOW_FAVORITES` / `target.type=collection`，其 `target` 只有 `title` 与 `description`（没有 `content`/`detail`/`excerpt`，所以此前正文渲染为空），作者字段叫 `creator` 而非 `author`，`target.url` 是 `https://api.zhihu.com/collections/{id}` 不能直接给阅读器。现已按 `followed_collection` 正式支持：类别纳入 `KnownCategories()`（此前裸 `collection` 会出现在 `missing_by_category` 里却被 `--include` 判为非法，自相矛盾），正文回退到 `description`，链接显式写为 `https://www.zhihu.com/collection/{id}`。这一次猜测值与真实值恰好相同，因此改名没有产生重复条目——Miniflux 中 21 个知乎 feed、共 2722 条已发布条目里 URL 含 `/collection/` 的命中为 0，也确认了不存在存量撞车。上述担忧对**其余**未知类型依然成立；现已补上 `target.creator` → `Author` 的回退，收藏夹条目不再无署名。更重要的发现是，`targetTypeFallback` 在 verb 未识别但 target.type 已知时会**静默**给出错误类别，比会告警的未知类型更危险。实测 Miniflux 的 2722 条已发布知乎条目中查出三处：`MEMBER_VOTE_PIN`（赞同了想法 52 条、赞同了视频 12 条，被归为 `pin`）、`添加了问题` 1 条（被归为 `followed_question`）、`赞同了项目` 1 条（被归为 `article`）。本次为 `MEMBER_VOTE_PIN` 新增 `voteup_pin` 类别，GUID 保持裸 id，避免这 64 条历史条目重复；同时让非空未知 verb 的兜底分支告警，空 verb 的正常兜底仍不告警。后两者的 verb 当时无法低成本获取，留待告警在日常运行中暴露。**该告警上线后第一次实机 dry-run（feed 87）即交出了其中一个**：`verb=MEMBER_ASK_QUESTION` / `action_text=添加了问题`，仍被兜底归为 `followed_question`；同次扫描的 7 条 followed_question 只告警 1 次，其余走正常的 `MEMBER_FOLLOW_QUESTION`，说明噪音水平可接受。现已为它单开 `asked_question` 类别，将 `MEMBER_ASK_QUESTION` 纳入 `verbCategoryMap`，不再触发告警。GUID 按默认规则使用 `asked_question_` 前缀，未并入裸 id 组；这与 `voteup_pin` 的处理不同，因为该类型仅有 1 条已发布条目，位于 feed 44 的 676 条更新条目之后，RSS 只发布最新 20 条，不会重发该条目，因此没有实际重复代价。这是「让静默兜底变成有声告警」首次产生闭环收益：告警上线后第一次实机运行就交出了此前拿不到的 verb，据此完成了修复。**同一个告警随后也交出了最后一个**：对 feed 77（`demonrebirth`）翻 600 条（该账号约每天 4~5 条，200 条只回溯到 2026-07-27，不足以够到 2026-06-21 那条）后拿到 `verb=MEMBER_VOTEUP_PROJECT` / `target_type=article` / `action_text=赞同了项目`，此前被兜底归为 `article`——即混进「自己发表的文章」，而该条 `target.author` 是 `ZZTJ` 而非账号本人，确属他人内容。这一条**没有**新开类别，与 `voteup_pin`、`asked_question` 两次的处理相反：上游自己就把 `target.type` 标为 `article`，URL 也是 `https://zhuanlan.zhihu.com/p/{id}`，正文是一篇活动投稿专栏，`voteup_article` 已能准确表达，再开一个仅 1 条样本的类别属于多余配置。因此只在 `verbCategoryMap` 增加 `MEMBER_VOTEUP_PROJECT → TypeVoteupArticle`，`KnownCategories()`、README 与 `--include` 合法值均不变。标题前缀取自 `action_text`（`zhihu.go:441`），与 category 无关，条目标题仍是 `[赞同了项目] 读通鉴`。GUID 由裸 id 变为 `voteup_article_` 前缀，无重复代价：该条在知乎动态流中深度 400+，RSS 只发布最新 20 条，永不重发；回填 `reconcile` 按 URL **或** GUID 去重，而 URL 不变。同时把 `MEMBER_VOTEUP_PROJECT` 纳入 `selfInteractableVerbs`——既然它并入 `voteup_article`，同一类别下两个 verb 的自互动过滤行为必须一致，否则 `--include-self-interaction` 的语义会破。至此对 21 个知乎 feed、2703 条已发布条目做的 `action_text` 全量普查中，13 种 `action_text` 全部落在已知类别上，无残留错分。仍存的缺口是：未知动态若同时带**空** verb 则不会告警（告警只在 `act.Verb != ""` 时触发）；该普查显示空 verb 目前只出现在「关注了问题」这条已知正确的兜底路径上，属零实例的假设问题，故不为其增加防御逻辑。

- 类别过滤与未知类型跳过均发生在 `reconcile` 之前，被排除的候选不参与对账，`Scanned` 保持「实际纳入对账的候选数」这一既有语义。

- `Run` 不为「候选 Category 为空但 IncludeCategories 非空」增加防御分支：CLI 层已拒绝把 `--include` 传给 Afdian，而 Afdian 的 `KnownCategories()` 返回 `nil` 会让整段类别逻辑跳过，该组合不可达。

### 知乎适配器

- 历史发现沿用 `/api/v3/moments/{id}/activities`，复用现有 `fetchActivities` 的签名、请求头与翻页协议，仅解除 `limit` 截断。

- 翻页终止条件补齐为四条：`paging.is_end` 为真、`paging.next` 为空、本页 `data` 为空、**next URL 已见过**。最后一条是对齐 Afdian 既有不变量的缺口修补——`getPostListWithProgress` 用 `seenCursors` 挡游标回绕，而知乎侧目前只有前两条出口，此前靠 `len(activities) < limit` 兜底，`limit` 一拆就没有兜底了。不设页数上限：`--max-pages` 之类的安全阀与「补齐全历史」直接冲突，限流由 `--request-interval` 承担，规模预判由 dry-run 承担。

  > **修订（2026-09-08，实测后）**：加上了 `--max-items`，默认 1000，`0` 表示不限制。原论证有两个漏洞，都是实测暴露的：其一，**「规模预判由 dry-run 承担」是循环论证**——dry-run 本身就要翻完整段历史，预判的代价等于它要预防的代价；实测对一个账号 dry-run 翻到 **1096 页 / 7674 条仍未出现 `is_end`**，光这一次预判就是 1096 次上游请求。其二，**中止不可用**：`Discover` 在 ctx 取消时返回 error，`Run` 随即 `return result, err`，`result.Scanned` 归零，杀进程等于这些请求全部作废，什么统计都拿不到。所以上限不是与目的冲突的安全阀，而是让"先小规模验证再放开"成为可能的前提。截断时通过 `ProgressFunc` 明确告知，不静默。

- 候选构造完全复用 `makeFeedItem`：标题（含 `[action_text]` 前缀）、链接、GUID、正文（含问题描述前缀与 `fixLazyImages` 处理）、作者、发布时间全部取自它的输出，`Candidate.Content` 直接取 `FeedItem.Content`，`Candidate.ID` 取 `FeedItem.GUID`，`Candidate.Category` 取 `deriveCategory` 的结果。不复制任何渲染逻辑——RSS 路径与回填路径的身份一致性完全依赖这一点。

- `Detail` 与 `Comments` 均实现为返回 `("", nil)`。`appendContent` 对空串是恒等操作，也不计入 `CommentFailures`，走的是既有正常分支，不产生虚假语义。

  > **修订（实现阶段）**：本节原写作「`Content` 恒非空，故 `Detail` 不可达，实现为返回错误以标记之」。该前提是错的：`renderContent`（zhihu.go:473-493）在 `target.content` 缺失、为 `null` 或无法解析时返回 `("", false)`，若 `target.detail` 与 `target.excerpt` 也为空，`makeFeedItem` 产出的正文就是空串。而 `Run` 对 `Detail` 错误的处理是 `return result, err`——照原设计，历史中一条正文为空的动态会中断整次回填。空正文是 RSS 路径对同一条动态的真实渲染结果，回填照样导入才是「两条路径逐字一致」，因此 `Detail` 返回 `("", nil)`。

- `KnownCategories()` 返回 `zhihu.go` 中已有的九个常量：`answer`、`article`、`pin`、`collected_answer`、`collected_article`、`collected_pin`、`voteup_answer`、`voteup_article`、`followed_question`，即 `verbCategoryMap` 的完整值域。

- `include_self_interaction` 默认 `false`，与路由默认一致，`--include-self-interaction` 可覆盖。该过滤在适配器的 `Discover` 内完成（复用现有 `isSelfInteraction`），不提升到共享模块——它不是 category 维度的概念。注意它只在最终类别集合允许 `voteup_*` / `collected_*` 通过时才有实际作用。

- `NewBackfillSource(cookie string, requestInterval time.Duration, includeSelfInteraction bool)` 在 **cookie 非空时**校验其含 `d_c0`，缺失即报错，不等到第一次请求。类别校验不在构造函数中——它已由 `Run` 通过 `KnownCategories()` 统一承担。

  > **修订（实现阶段）**：原写作无条件校验，与 `ActionList` 冲突。CLI 只为非 List 动作读取 cookie（`--list-feeds` 仅需 `MINIFLUX_API_TOKEN`），而 `Run` 的 `ActionList` 分支仍需要一个 `Source` 来跑 `FeedIdentity`，无条件校验会让 `--source zhihu --list-feeds` 必然失败。真正的不变量是「不发请求的动作不需要凭证；发请求的动作在启动阶段就必须持有合法凭证」——List 只做 feed URL 字符串解析，不触碰 scraper。

- `Discover` 通过 `ProgressFunc` 汇报翻页进度（页码、本页条数、累计条数），与 Afdian 对称。

- 知乎请求串行，共享一个限速器，默认最小间隔 1s。Miniflux 请求不消耗该限速额度。

### 身份与去重

- `Candidate.ID` 即 `makeFeedItem` 生成的 GUID：answer / article / pin 三类为裸 `targetID`，其余类别为 `{category}_{targetID}`。这个规则不能在回填侧重新推导，必须来自同一函数，否则 Miniflux 的 `external_id` 哈希与 RSS 摄入路径分叉，去重失效。

- `reconcile` 的「URL 命中或 GUID 哈希命中即视为已存在」保持不变。这带来一个**明示的期望行为**：`voteup_answer` / `collected_answer` 与原创 `answer` 的 target 是同一篇回答，`makeFeedItem` 给出的链接完全相同，因此 feed 中已存在其中一种时，另一种会被 URL 判重跳过。这是有意为之——同一篇回答在阅读器里出现两次是纯噪音。记录于此以免将来被当作 bug 修掉。

- 回填路径**不**经过 bluemonday 消毒，与 Afdian 现状一致：`feed.Generate` 只作用于 RSS 路径，直接导入的内容由 Miniflux 自行消毒。

### 不在本次改动范围内的重构

- `docs/architecture-review.md` 条目中列出的假想接缝清理（zhihu 的 `fetchActivitiesFn`、afdian 的四个函数字段）不在本次范围内。新增的知乎回填适配器不会再增加此类字段，但既有字段保持原样。

## Testing Decisions

- 阶段一的验收使用现有 CLI 测试改造而成：`TestAfdianBackfillCommandBuildsConfig`、`TestAfdianBackfillCommandRejectsUnsafeOrMissingInputs`、`TestRunAfdianBackfillListsEligibleFeedsWithoutServerConfig` 迁移到统一命令形态。完成标准是 `go test ./...` 全绿，且 `backfill --source afdian --list-feeds` 的输出与旧命令逐字一致。

- 阶段一不为尚无消费者的接口字段编写测试。`Candidate.Content`、`Candidate.Category`、`Request.IncludeCategories`、`Result` 新增计数的测试与知乎适配器一同在阶段二落地。

- CLI 测试覆盖：`--source` 缺失报错、未知数据源报错、专属 flag 传给 afdian 报错、`--list-feeds` 与 `--feed-id` 互斥、必需环境变量缺失报错、feed ID 必须为正整数、`--request-interval` 下限、退出码。

- 知乎适配器测试通过 `httptest` 覆盖包级 `zhihuHostURL` 驱动，不新增函数字段接缝。可行性已确认：知乎 `paging.next` 返回绝对 URL，假服务器返回自身地址即可驱动翻页；`GetSignature` 对任意 URL 都能计算，不需要真实响应配合。

- 翻页测试覆盖：翻到 `is_end` 自然结束、`next` 为空结束、空 `data` 结束、**重复 next URL 时终止且不再发起请求**。

- 类别测试覆盖：`--include` 覆盖 feed URL 的 include、feed URL include 在无 `--include` 时生效、两者皆空时不过滤、`--include` 含非法值报错且未发起任何翻页请求、feed URL 含非法值报错、`ActionList` 不因非法 include 而排除该 feed。

- 未知类别测试：构造一条 verb 与 target.type 均不在映射表中的动态，断言它被跳过、`SkippedUnknown` 递增、产生 `Warning`、不计入 `Scanned`、不产生导入。

  > **修订（2026-09-08，实测后）**：断言反转为「照常导入、计入 `Scanned`、类别取 `target.type` 并出现在 `MissingByCategory`、不产生 `Warning`」；另加一条断言显式 `--include` 仍排除它。

- 正文来源测试：断言 `Content` 非空的候选不触发 `Detail` 调用，且导入 Miniflux 的正文与 RSS 路径对同一条动态生成的正文逐字相同。

- 身份一致性测试：断言 `Candidate.ID` 与 RSS 路径对同一条动态生成的 GUID 相同，覆盖 answer / article / pin 的裸 ID 形式与其余类别的 `{category}_{id}` 形式。

- URL 判重碰撞测试：feed 中已存在某篇 answer 时，回填同一 target 的 `voteup_answer` 被计为已存在且不产生导入——断言这个期望行为，而非绕过它。

- dry-run 测试：断言完整翻页、准确的 scanned/existing/missing 计数、`MissingByCategory` 分组正确、`SkippedUnknown` 正确，且零 detail / 零 comments / 零 import 调用。

  > **修订（2026-09-08，实测后）**：去掉 `SkippedUnknown` 断言；另加 `TestBackfillStopsAtMaxItems`——上游永不返回 `is_end` 时，`--max-items` 是唯一的终止条件。

- Afdian 回归测试：`KnownCategories()` 返回 `nil` 时类别逻辑整段跳过；带 `?include=` 的 Afdian feed URL 不会导致候选被过滤为空。

- 自互动过滤测试：默认过滤，`--include-self-interaction` 放行，且仅在类别集合允许 `voteup_*` / `collected_*` 时可观察。

- 测试沿用项目既有的标准库 `testing` + `httptest` 风格，不引入断言库，不发起真实的知乎或 Miniflux 请求，不需要真实凭证。

## Out of Scope

- 引入 `/api/v4/members/{id}/answers`、`/api/v4/members/{id}/articles`、`api.zhihu.com/pins/{id}/moments` 作为历史来源。
- 为知乎抓取评论或渲染评论。
- 修改知乎路由在 RSS 路径上的任何行为，包括条目顺序、默认条数、内容渲染。
- 为回填路径新增图片归档、图片 URL 重写、enclosure 合成或媒体存储。
- ~~`--max-pages`、时间下界或任何形式的历史数量上限。~~

  > **修订（2026-09-08，实测后）**：`--max-items` 已进入范围，理由见上。时间下界仍不在范围内。
- 保留 `afdian-backfill` 兼容别名。
- 一次调用回填多个 feed，或隐式 `--all` 模式。
- 清理 `docs/architecture-review.md` 中列出的假想接缝（`fetchActivitiesFn` 及 afdian 的四个函数字段）。
- 为回填路径引入 bluemonday 消毒或改变 Afdian 回填的现有内容处理。
- 支持非 Miniflux 的导入后端。
- 跨进程锁、本地断点续跑状态或新增数据库。

## Further Notes

- 「知乎动态流能翻多深」是本设计中唯一未经测量的假设。首次实现完成后应先对一个真实 feed 运行 `--dry-run`，观察 `scanned` 与缺失时间范围的下界。如果动态流存在明显的保留窗口，再评估是否需要引入 members 系列端点作为第二来源——那将是一份独立设计，需要处理三套新解析以及与现有 GUID 规则的对齐。

- RSSHub 的 activities 请求 URL 比 RSSGen 多一个 `&ws_qiangzhisafe=0` 参数，用途不明。若深翻页时遭遇风控，这是第一个值得尝试的旋钮。

- 知乎每次请求都要额外计算 `x-zse-96` 签名，且 `internal/sign/zhihu` 是上游 JS 改版时最先失效的模块。深翻页会显著放大签名失效的暴露面，长时间运行的回填遇到批量 4xx 时应优先怀疑签名而非风控。

- 本设计与 `2026-08-26-afdian-history-backfill-design.md` 的关系：那份文档定义了 backfill 深模块的原始形状与 Afdian 侧的全部决策，本文档在其之上做接口扩展。凡与 Afdian 行为相关之处，以那份文档为准；凡涉及本次接口扩展与知乎行为之处，以本文档为准。
