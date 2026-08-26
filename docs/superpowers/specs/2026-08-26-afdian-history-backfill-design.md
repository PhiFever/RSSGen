---
name: afdian-history-backfill
description: 保留 Miniflux 拉取最新 RSS 的常态路径，新增一次性 Afdian 全历史补缺回填，并移除 SQLite 文章缓存
type: project
status: draft
date: 2026-08-26
related: 2026-08-26-miniflux-only-collector-design.md
---

# Afdian 历史回填与 SQLite 移除

## Problem Statement

RSSGen 当前由 Miniflux 定时拉取 RSS/Atom feed。Afdian 路由默认只返回最新 20 篇文章，这适合日常订阅，却无法把付费后有权访问的全部历史内容补进现有 Miniflux feed。

现有 Afdian 路由还把文章正文持久化到本地 SQLite。Miniflux 已经是最终阅读条目的持久化位置，继续维护 SQLite 文章副本会增加配置、运行时连接逻辑、容器卷和测试复杂度。与此同时，当前配置示例把 feed 的 `limit: 0` 描述为“获取全部文章”，但实际循环在 `limit == 0` 时不会抓取任何文章；不能把这个配置误当成可靠的历史回填入口。

本阶段不执行完整的 Miniflux-only 迁移。操作者希望保留现有的常态拉取模型：RSSGen 继续提供最新 feed，Miniflux 继续轮询。历史回填则是一项明确、一次性的前台操作，由操作者指定现有 Miniflux feed，RSSGen 主动遍历 Afdian 全部历史，并只把 Miniflux 尚未保存的条目推入该 feed。

## Solution

RSSGen 保留两种且仅有两种产品运行方式：

1. 无参数启动时维持现有 server 行为，继续提供 RSS/Atom，并由 Miniflux 拉取最新内容。默认 feed 条目数仍为 20，HTTP 调用方仍可用现有查询参数临时覆盖。
2. 显式运行 Afdian backfill 前台命令时，RSSGen 不启动 server，也不读取 server 的路由配置。该命令连接 Miniflux，列出或验证目标 feed，从 feed 的 `feed_url` 推导 Afdian author slug，查询 feed 中全部已有条目，再把 Afdian 全历史中缺少的文章按新到旧导入 Miniflux。

回填没有首次水位、时间截点或历史数量上限。Miniflux feed 为空时，全部可发现历史都是待回填候选。feed 已有条目时，回填仍扫描完整上游历史，以便补齐任意中间缺口，而不只是补“当前最旧条目之前”的连续区间。只有缺失候选才会触发详情和评论请求。

回填以 Afdian `post_id` 作为稳定来源 ID。现有 RSS/Atom 路径已经把 `post_id` 输出为条目 ID/GUID；Entry Import 也发送同一个值作为 `external_id`。canonical article URL 同时作为迁移兼容的存在判断依据。Miniflux 的内建去重是并发竞态下的最后保险，不额外引入跨进程锁。

本阶段同时完整移除 SQLite 文章缓存及其连接逻辑。内存 feed cache 保留；用户磁盘上已有的 SQLite 文件不会被程序自动删除。

## User Stories

1. As an RSSGen operator, I want Miniflux to keep pulling RSSGen during normal operation, so that my existing subscription workflow continues to work.
2. As an RSSGen operator, I want normal Afdian feeds to default to the latest 20 articles, so that routine polling remains bounded.
3. As an RSSGen operator, I want the existing HTTP `limit` override to remain available, so that I can temporarily inspect a different number of recent items.
4. As an RSSGen operator, I want feed-level `limit` removed from YAML configuration, so that server configuration does not pretend to provide historical backfill.
5. As an RSSGen operator, I want Afdian history backfill to be an explicit foreground command, so that a long paid-content crawl never starts accidentally with the server.
6. As an RSSGen operator, I want backfill to run without loading server route configuration, so that one-time credentials and targets stay separate from daemon configuration.
7. As an RSSGen operator, I want to provide a Miniflux URL explicitly, so that I always know which instance the one-time task will modify.
8. As an RSSGen operator, I want Miniflux API credentials to come from an environment variable, so that tokens do not appear in process arguments.
9. As an RSSGen operator, I want Afdian credentials to come from an environment variable, so that paid-session cookies do not appear in process arguments.
10. As an RSSGen operator, I want to list eligible Afdian feeds before choosing one, so that I can discover the numeric Miniflux feed ID safely.
11. As an RSSGen operator, I want the feed listing to show ID, title, and feed URL, so that I can distinguish similar subscriptions.
12. As an RSSGen operator, I want feed listing to exclude unrelated routes, so that selecting an Afdian target is straightforward.
13. As an RSSGen operator, I want to identify the target with an explicit Miniflux feed ID, so that backfill never guesses by title.
14. As an RSSGen operator, I want backfill to derive the Afdian author slug from the target feed URL, so that I do not provide the same identity twice.
15. As an RSSGen operator, I want backfill to reject a feed whose URL is not an RSSGen Afdian route, so that articles cannot be written into an unrelated subscription accidentally.
16. As an RSSGen operator, I want backfill to work when the target feed is empty, so that I can import the complete paid archive from a clean start.
17. As an RSSGen operator, I want backfill to scan Afdian until the upstream list reaches its natural end, so that no artificial initial watermark truncates paid history.
18. As an RSSGen operator, I want backfill to tolerate Miniflux entries that no longer exist upstream, so that author-deleted posts do not invalidate the rest of the archive.
19. As an RSSGen operator, I want backfill to compare against all existing Miniflux entries, so that it can repair gaps anywhere in the historical range.
20. As an RSSGen operator, I want list discovery to finish successfully before detail imports begin, so that a broken upstream pagination run does not act on an incomplete view of history.
21. As an RSSGen operator, I want details fetched only for missing posts, so that paid Afdian requests stay low.
22. As an RSSGen operator, I want available comments appended using the current rendering behavior, so that imported history retains the established reading experience.
23. As an RSSGen operator, I want a comment failure to preserve and import the article body, so that optional discussion data does not block paid content.
24. As an RSSGen operator, I want a detail failure to stop the run, so that the backfill does not silently create a historical gap.
25. As an RSSGen operator, I want a Miniflux import failure to stop the run, so that failures remain visible and resumable.
26. As an RSSGen operator, I want transient Afdian and Miniflux failures retried conservatively, so that a long run does not fail on the first brief network interruption.
27. As an RSSGen operator, I want the command to exit non-zero after retries are exhausted, so that scripts can detect an incomplete backfill.
28. As an RSSGen operator, I want successful imports preserved when a later post fails, so that rerunning resumes through Miniflux state rather than starting over.
29. As an RSSGen operator, I want duplicate import responses counted as existing rather than fatal, so that a narrow polling race is harmless.
30. As an RSSGen operator, I want historical entries imported from newer to older, so that an interrupted run advances a naturally resumable backward frontier.
31. As an RSSGen operator, I want historical imports marked unread, so that paid archive entries appear in my reading workflow.
32. As an RSSGen operator, I want stable Afdian post IDs sent as Miniflux external IDs, so that RSS pulling and direct import converge on the same identity.
33. As an RSSGen operator, I want canonical Afdian article URLs sent on every import, so that entries remain recognizable across ingestion paths.
34. As an RSSGen operator, I want a dry-run mode, so that I can see the size and date range of missing history before fetching details or writing entries.
35. As an RSSGen operator, I want dry-run to report scanned, existing, and missing counts, so that I can verify the target and expected work.
36. As an RSSGen operator, I want Afdian requests to be serial, so that historical crawling remains conservative.
37. As an RSSGen operator, I want at least one second between Afdian requests, so that the backfill is no more aggressive than intended.
38. As an RSSGen operator, I want to increase the request interval explicitly, so that I can run even more slowly when needed.
39. As an RSSGen operator, I want Miniflux requests excluded from the Afdian interval, so that local bookkeeping does not unnecessarily double backfill time.
40. As an RSSGen operator, I want SQLite article persistence removed, so that Miniflux is the only durable article store used by this feature.
41. As an RSSGen operator, I want existing SQLite files left untouched, so that code removal never destroys data unexpectedly.
42. As an RSSGen operator, I want the in-memory feed cache retained, so that normal RSS requests can still avoid repeated work within a process lifetime.
43. As an RSSGen operator, I want startup preheating to follow its explicit configuration on every restart, so that the removal of a hidden SQLite marker does not create ambiguous behavior.
44. As an RSSGen maintainer, I want one reusable backfill module, so that a later Zhihu adapter can reuse Miniflux reconciliation, retries, ordering, and statistics.
45. As an RSSGen maintainer, I want backfill tested without live Afdian or Miniflux calls, so that the suite remains deterministic and safe.
46. As an RSSGen maintainer, I want the focused phase documented separately from the final Miniflux-only vision, so that staged implementation is not mistaken for an incomplete final migration.

## Implementation Decisions

- This is an interim hybrid architecture, not the final Miniflux-only architecture. Normal operation remains Miniflux-pulls-RSSGen; only explicit historical backfill pushes entries to Miniflux.

- The binary has two product modes. With no backfill arguments it follows the current server startup path and loads the existing server configuration. With the explicit Afdian backfill command it parses backfill arguments before server configuration loading, does not start the HTTP server or refresher, and exits when the foreground task completes.

- No generic one-shot latest-sync command and no active collector daemon are introduced. The only push mode in this phase is historical reconciliation.

- The backfill command accepts an explicit Miniflux base URL. Listing mode requires the Miniflux token but not an Afdian cookie. Dry-run and execute modes require both the Miniflux token and Afdian cookie. Secrets are read from `MINIFLUX_API_TOKEN` and `AFDIAN_COOKIE` and must never be logged.

- Listing mode queries all feeds visible to the authenticated Miniflux user and displays numeric ID, title, and feed URL only for feeds whose URL path represents an RSSGen Afdian feed.

- Execute and dry-run modes require a positive numeric feed ID. The command fetches that exact Miniflux feed, validates that its `feed_url` path matches the Afdian route shape, URL-decodes the single author slug segment, and rejects missing, extra, or unrelated path segments. Titles are never used for target selection.

- The Miniflux module owns the `/v1` prefix, `X-Auth-Token` authentication, JSON shapes, pagination, timeouts, and HTTP error mapping. Callers provide a base instance URL without needing to know endpoint layout.

- Existing Miniflux entries are read across all statuses and all pages. Reconciliation records canonical article URLs and Miniflux entry hashes. A candidate is treated as existing when its canonical URL matches or when its `post_id`-derived hash matches an existing entry.

- The identity compatibility is intentional: the current RSS/Atom output uses Afdian `post_id` as entry ID/GUID, Miniflux hashes that source ID during feed parsing, and Entry Import hashes the supplied `external_id`. Direct imports therefore send `external_id = post_id` and the same canonical Afdian article URL used by the RSS route.

- Miniflux's duplicate handling is a last line of defense rather than the primary existence query. An import response of `201 Created` counts as imported. A `200 OK` response counts as already existing and the run continues. Other unexpected responses are errors.

- No cross-process file or distributed lock is added. Normal RSS polling and backfill normally work on different ranges; a remaining race converges through the shared `post_id` hash. Both ingestion paths create new items as unread, minimizing status disagreement in that narrow window.

- Afdian discovery resolves the author slug to the upstream user ID, then follows the existing newest-first `publish_sn` pagination until the upstream explicitly signals the end through an empty response/list or a terminal cursor. The backfill path has no article-count limit. Repeated post IDs or cursor overlap must not create duplicate candidates.

- Discovery is a separate phase from detail fetching and importing. The command must reach a natural upstream end before it begins fetching details. A list request or parse error after retries aborts without starting the import phase.

- Miniflux entries that are absent from the current upstream list do not cause a watermark-not-found failure. They may represent author-deleted content. Reconciliation is driven only by upstream candidates that are missing from Miniflux.

- Dry-run stops after reconciliation. It performs feed validation, existing-entry pagination, author resolution, and complete Afdian list discovery, but performs no detail requests, comment requests, or Miniflux imports. Its result includes scanned, existing, missing, and duplicate-candidate counts plus the missing publication-time range when available.

- Execute mode processes missing candidates in newest-to-oldest order. For each candidate it fetches detail, attempts to fetch comments, builds the normalized article, and imports it before moving to the next candidate. Successful earlier imports remain durable if a later candidate fails; a rerun rebuilds the Miniflux existence set and skips them.

- New imports provide canonical URL, title, author, HTML content, publication timestamp, `status = unread`, and `external_id = post_id`. Current comment rendering and HTML append behavior are reused.

- Detail errors are hard failures. Comment errors are soft failures: they are reported as warnings and statistics, while the article body continues to import. Import errors are hard failures. The run never skips a detail/import failure and continues toward older entries.

- Afdian list, detail, and comment operations are strictly serial and share one rate limiter. The default minimum interval is one second. The CLI may request a longer interval but cannot reduce it below one second. Miniflux HTTP calls do not consume this source rate limit.

- Afdian and Miniflux network operations retry at most three times with conservative exponential backoff. Retries do not change item ordering. After the third failed attempt, the foreground command exits non-zero. Comment exhaustion remains a soft failure as described above.

- The primary backfill module is deliberately deep. Its caller-facing interface is one run operation whose request selects list, dry-run, or execute behavior and whose result contains observable counts and errors. Feed lookup, slug derivation, entry reconciliation, ordering, retry policy, and progress accounting remain inside the module.

- The highest seam is the backfill module interface. Afdian and Miniflux are true external dependencies represented by narrow internal ports at that seam. Production HTTP adapters and deterministic fake adapters satisfy those ports. The CLI does not learn pagination, deduplication, or import sequencing rules.

- The normalized candidate/article shape remains source-neutral enough for a later Zhihu adapter, but this phase does not add Zhihu behavior or a premature media interface.

- The existing Afdian server route and the backfill adapter share the Afdian upstream parsing and rendering implementation rather than duplicating endpoint contracts. The route remains bounded by the normal feed limit; the backfill adapter exposes exhaustive discovery to the deep backfill module.

- SQLite is removed completely from the running code: the SQLite module and tests, driver dependency, storage configuration, initialization/close lifecycle, article-store interfaces, pipeline/refresher wiring, Afdian cache reads/writes, and persistence-aware preheat checks are removed.

- SQLite removal does not remove the in-memory feed cache. When an Afdian feed cache entry expires, the normal server path may fetch recent details again. When startup preheating is enabled, it runs on each process start because no durable article marker remains.

- Existing SQLite database files are not deleted or migrated automatically. Container and documentation references that exist only for SQLite are removed, while future media persistence will be designed separately.

- Feed-level YAML `limit` is removed. The shared HTTP fetch option and `limit` query parameter remain, with the current default of 20. Refresher-configured feeds use that default rather than a persisted per-feed limit. Backfill ignores the HTTP limit mechanism.

- Image treatment is unchanged in this phase. Backfill does not append Afdian `pics`, synthesize enclosures, archive images, or introduce media storage. It imports only the detail HTML and rendered comments available through the current content path.

## Testing Decisions

- Tests assert observable behavior through the highest backfill seam: returned counts/errors, Afdian operations requested, and Miniflux entries imported. They do not assert private helper calls or internal collection shapes.

- The principal behavior tests use fake Afdian and Miniflux adapters. These tests cover list, dry-run, and execute actions through the same backfill module interface used by the CLI.

- An empty Miniflux feed test proves that complete upstream history is discovered, every candidate detail is fetched, and entries are imported newest-to-oldest as unread.

- A populated feed test proves that all Miniflux entry pages are considered, existing candidates do not trigger detail/comment calls, and only missing candidates are imported.

- A gap-repair test places existing entries on both sides of a missing historical post and proves that full reconciliation imports the gap rather than stopping at the oldest or newest existing entry.

- Identity tests cover canonical URL matches, `post_id` hash matches, duplicate upstream candidates, `201 Created`, and `200 OK` races. A `200` result increments existing/duplicate statistics and does not fail the run.

- Ordering tests prove that execute mode imports missing history newest-to-oldest, while the normal RSS route retains its existing list order and default bounded behavior.

- Discovery failure tests prove that an Afdian list request or parse failure prevents the detail/import phase from starting. Empty lists and terminal cursors prove natural successful completion.

- Detail failure tests prove that the current candidate stops the run after retry exhaustion, no older candidate is fetched or imported, and previously successful newer imports remain observable.

- Comment failure tests prove that retry exhaustion records a warning/failure count, preserves the detail body, imports the article, and continues to the next older candidate.

- Miniflux import failure tests prove fail-fast ordering and non-zero foreground completion. Unexpected non-2xx responses include bounded diagnostic response content without leaking tokens.

- Retry tests use injected deterministic waiting behavior rather than wall-clock sleeps. They assert at most three attempts, exponential ordering, and no parallel Afdian calls.

- Rate-limit tests assert serialized Afdian request ordering and enforcement of the minimum accepted CLI interval. They do not make the suite sleep for real seconds.

- Dry-run tests prove complete list discovery and accurate scanned/existing/missing/time-range results with zero detail, comment, or import calls.

- Feed-listing tests prove that only valid Afdian feed URLs are shown and that ID, title, and feed URL are present. Feed-validation tests cover wrong routes, malformed URLs, missing slug segments, extra path segments, and URL-escaped slugs.

- CLI tests cover argument dispatch before server configuration loading, mutually exclusive list/execute inputs, required environment variables, positive feed IDs, a minimum one-second request interval, exit codes, and the unchanged no-argument server path.

- Miniflux HTTP adapter tests use an HTTP test server to assert `/v1` paths, `X-Auth-Token`, feed lookup/listing, entry pagination parameters, request bodies, `200` versus `201`, timeout/error mapping, and token redaction.

- Afdian adapter tests retain current upstream fixtures and behavior around author resolution, newest-first pagination, `post_id`, detail extraction, comment rendering, upstream business errors, and alias-independent identity. New exhaustive-discovery tests specifically cover behavior without a limit.

- SQLite-removal tests are replace-not-layer work: obsolete store/cache tests and fake article-store scaffolding are deleted, while pipeline, refresher, route, configuration, and runtime tests are updated to exercise their simpler interfaces.

- Existing route, pipeline, refresher, feed generation, notifier, health, and main runtime tests are prior art. The current standard-library `testing` and `httptest` style remains the project convention.

- Normal tests make no live calls to Afdian or Miniflux and require no real credentials. A manual smoke test may be documented separately but is not part of the automated suite.

## Out of Scope

- Replacing normal Miniflux polling with an active collector daemon.
- Adding a generic `collect-once` command for latest entries.
- Removing RSS/Atom serving, the HTTP server, refresher, notifier, health handling, or the in-memory feed cache.
- Implementing the final Miniflux-only architecture described by the related broader design.
- Creating, discovering by title, disabling, or provisioning Miniflux feeds.
- Implementing Zhihu backfill or changing Zhihu content behavior.
- Appending Afdian `pics` to HTML, preserving enclosures through another mechanism, downloading images, rewriting image URLs, or adding a media store/server.
- Updating already imported content after a later media implementation.
- Archiving video or audio resources.
- Adding local durable backfill checkpoints, a new database, a distributed lock, or a shared lock file.
- Automatically deleting or migrating an existing SQLite database file.
- Running multiple feed backfills from one invocation or adding an implicit `--all` mode.
- Changing Afdian comment pagination or rendering beyond reuse of current behavior.
- Building a browser UI or remote administration endpoint for backfill.
- Supporting non-Miniflux import backends or multi-user Miniflux tenancy beyond the supplied token.

## Further Notes

- Miniflux Entry Import is available beginning with Miniflux 2.2.16. The target instance must support that endpoint and API-token authentication.

- The Miniflux base URL is the instance root; the adapter adds `/v1`. Trailing-slash normalization is an implementation detail hidden by the Miniflux module.

- The intended discovery flow is: run the Afdian feed-listing action, select a numeric feed ID, run dry-run, then run execute. These are actions of the same foreground backfill mode, not additional product modes.

- A feed that is empty in Miniflux has no special initial-watermark requirement. Full upstream exhaustion is the expected and intentional behavior because Afdian access is paid and the operator explicitly initiated backfill.

- Normal RSS polling and direct import share `post_id` identity in the current RSSGen and Miniflux implementations. The implementation must still query existing entries before detail calls and must not use Entry Import itself as the routine existence query.

- If Miniflux cleanup has tombstoned an old entry, a direct re-import may be rejected by Miniflux. That response is an import failure and stops the run; this phase does not bypass Miniflux cleanup or tombstone policy.

- Removing SQLite means recent Afdian details can be requested again after an in-memory feed cache miss or process restart. This is an accepted tradeoff in the staged architecture.

- This focused spec records the decisions reached for the first Afdian phase. Where it conflicts with the Afdian portions of the broader Miniflux-only collector design, this focused spec governs the current implementation. The broader document remains the reference for later Zhihu media work and a possible final ownership migration.
