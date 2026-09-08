---
name: miniflux-only-collector
description: 将 RSSGen 破坏性迁移为仅支持 Miniflux 后端的采集与导入服务
type: project
status: ready-for-agent
date: 2026-08-26
---

# Miniflux 单后端采集模式

## Problem Statement

当前 RSSGen 的主要交付物是 RSS/Atom XML，Miniflux 通过轮询 RSSGen 来发现新文章。这个模式让状态被拆在多个地方：RSSGen 的本地 SQLite 保存部分文章正文，RSSGen 的 feed 缓存保存派生 XML，Miniflux 自己又保存最终阅读条目。

对爱发电路由来说，冷启动或缓存失效后仍需要遍历列表页并逐篇拉详情，才能重新构造 RSS 输出。Miniflux 已经具备最终条目存储能力，但 RSSGen 仍在维护一套中间态，因此系统复杂度和抓取成本都偏高。

对知乎路由来说，正文中的图片仍依赖知乎外部 CDN。即使正文已经进入 Miniflux，只要外部图片 URL 失效，历史文章的图片仍会无法加载。Miniflux 内置 media proxy 只是代理外部资源，不是持久图床，不能解决源 CDN 失效后的长期可读性问题。

用户希望做一次针对 Miniflux 的破坏性变更：RSSGen 不再作为通用 RSS 生成框架，而是变成一个只面向当前设备上 Miniflux 实例的慢速采集器、媒体归档器和条目导入器。

## Solution

RSSGen 改造成 Miniflux-only collector。数据源路由继续负责理解爱发电、知乎等上游 API，但不再以 RSS/Atom XML 作为主要输出。新的采集层负责按配置逐个同步 Miniflux 中的已有订阅源：先从 Miniflux 查询目标 feed 的最新已导入条目，再从上游按时间倒序发现新文章，遇到已录入条目后停止，最后将新文章按旧到新的顺序导入 Miniflux。

Miniflux 成为唯一后端状态来源。爱发电路由不再使用本地 SQLite 保存正文；是否已经抓过、是否需要继续翻页，由目标 Miniflux feed 中的条目决定。导入使用 Miniflux Entry Import API，将文章写入指定的现有 feed。写入时提供 canonical URL 和 stable external_id，但不把导入接口当作查重探针。

知乎图片归档由 RSSGen 自己承担。采集器在导入前扫描正文 HTML 中的图片 URL，下载允许的图片资源，按内容哈希写入本地持久目录，并将正文里的图片地址改写为当前设备可访问的本地媒体 URL。RSSGen 仍保留一个很薄的 HTTP 服务，用于 `/media`、`/status`、`/health` 等运行接口；RSS/Atom feed 服务不再是产品主路径。

运行方式增加面向采集的二进制模式：默认作为 daemon 缓慢循环采集并导入，也支持一次性 `collect-once`，便于手动补数据、排障和定时任务集成。

## User Stories

1. As an RSSGen operator, I want RSSGen to support Miniflux as the only persistence backend, so that I do not maintain duplicate article stores.
2. As an RSSGen operator, I want to configure a Miniflux base URL and API token, so that the collector can write directly to my local Miniflux instance.
3. As an RSSGen operator, I want each configured source feed to map to an existing Miniflux feed ID, so that imported entries appear under the subscription I already use.
4. As an RSSGen operator, I want RSSGen to avoid serving synthetic RSS as the main interface, so that Miniflux no longer has to poll RSSGen to discover generated XML.
5. As an RSSGen operator, I want RSSGen to discover new Afdian posts by newest-first pagination, so that routine syncs only touch recent upstream pages.
6. As an RSSGen operator, I want Afdian sync to stop after it sees an entry already recorded in Miniflux, so that it does not traverse old list pages on every cold start.
7. As an RSSGen operator, I want Afdian details to be fetched only for genuinely new posts, so that detail API calls stay low.
8. As an RSSGen operator, I want Afdian comments to be attached before import when available, so that imported entries retain the current RSSGen reading experience.
9. As an RSSGen operator, I want Afdian local SQLite article caching removed, so that Miniflux is the durable article store.
10. As an RSSGen operator, I want sync to import new entries oldest-first, so that Miniflux entry ordering and unread flow are natural.
11. As an RSSGen operator, I want imported entries to be marked unread by default, so that newly discovered articles show up like normal feed updates.
12. As an RSSGen operator, I want stable source IDs to be sent as Miniflux external IDs, so that duplicate imports are idempotent inside a feed.
13. As an RSSGen operator, I want duplicate checks to use existing Miniflux entries before import, so that duplicate imports do not accidentally mutate read/unread state.
14. As an RSSGen operator, I want the first sync to have an explicit initial limit or cutoff, so that a missing watermark does not import unlimited history.
15. As an RSSGen operator, I want sync to fail closed when the watermark cannot be found within the configured scan cap, so that accidental history floods are avoided.
16. As an RSSGen operator, I want source request rates to remain slow and conservative, so that Afdian and Zhihu scraping behavior does not become more aggressive.
17. As an RSSGen operator, I want Zhihu activities to keep the existing category and stable ID semantics, so that old route behavior remains recognizable.
18. As an RSSGen operator, I want Zhihu answer, article, and pin content to be imported with the same body selection rules currently used by RSSGen, so that content quality does not regress.
19. As an RSSGen operator, I want Zhihu lazy-image placeholders to be resolved before media archiving, so that archived images are the real image URLs.
20. As an RSSGen operator, I want Zhihu images to be stored locally before import, so that historical Miniflux entries remain readable after Zhihu CDN URLs expire.
21. As an RSSGen operator, I want local media URLs to be stable and content-addressed, so that the same image is not repeatedly stored under different names.
22. As an RSSGen operator, I want the media directory to live on a Docker volume, so that archived images survive container replacement.
23. As an RSSGen operator, I want the configured media public URL to be the URL browsers can actually load, so that Miniflux entry content renders images outside the container network.
24. As an RSSGen operator, I want media downloads to validate content type and size, so that broken or unexpectedly large resources are not archived blindly.
25. As an RSSGen operator, I want image archiving failures to stop importing the affected article by default, so that Miniflux does not persist half-archived content.
26. As an RSSGen operator, I want a one-shot collection mode, so that I can manually backfill or test a route without running a long-lived daemon.
27. As an RSSGen operator, I want a daemon collection mode, so that configured feeds are refreshed slowly in the background.
28. As an RSSGen operator, I want `/status` to report Miniflux import counts, source failures, media failures, and last successful sync, so that I can operate the collector without reading logs first.
29. As an RSSGen operator, I want failed syncs to retry later from the same Miniflux watermark, so that transient failures do not create gaps.
30. As an RSSGen operator, I want source routes to stay independently testable, so that changes to Miniflux import do not make scraper behavior harder to validate.
31. As an RSSGen operator, I want Miniflux feed creation to remain explicit, so that the collector never writes into the wrong feed by guessing.
32. As an RSSGen operator, I want a documented migration path from the old RSS-pull deployment, so that existing Miniflux subscriptions can be disabled and taken over safely.

## Implementation Decisions

- This is a destructive backend narrowing. RSSGen will target one backend: Miniflux. Generic RSS/Atom serving, generic feed cache behavior, and local article persistence are no longer architectural centers.

- Keep source-specific route logic separate from Miniflux writes. Routes should expose normalized article candidates; a collector service should own watermark lookup, pagination stop decisions, media archiving, import ordering, retry behavior, and status accounting. This keeps the Miniflux API boundary in one place instead of scattering `POST` calls through each route.

- Introduce a Miniflux client abstraction with these capabilities: fetch recent entries for a feed, import one entry, optionally list feeds for validation, and optionally import OPML for provisioning synthetic disabled feeds. The client is the only code that understands Miniflux HTTP status codes and JSON shapes.

- Import entries through Miniflux Entry Import API. The API requires `url`; supports `title`, `author`, `content`, `published_at`, `status`, `starred`, `tags`, `external_id`, and `comments_url`; returns `201 Created` for new entries and `200 OK` for entries that already exist. The implementation should call the API under the Miniflux `/v1` API prefix.

- New imports should set `status` to `unread` by default. This matches the expected reader workflow more closely than the import endpoint's default behavior.

- Do not use Entry Import as an existence query. Re-importing an existing entry can still mutate entry status, and Miniflux does not overwrite the existing content on duplicate import. The collector should query existing entries first and import only candidates it believes are new.

- Duplicate and early-stop logic should use canonical article URLs as the migration-safe watermark. Existing historical entries that came from RSS/Atom and future entries imported through Entry Import may not share the same Miniflux internal hash input, but their canonical URL should remain stable across the migration boundary.

- For new imported entries, still send `external_id` using the current stable source ID. This gives Miniflux a stronger idempotency key for future collector-managed imports within the same feed.

- Stable source IDs remain route-defined. Afdian uses `post_id`. Zhihu uses `target.id` for own answer/article/pin items, and `{category}_{target.id}` for collected/voteup/followed interactions.

- A configured source feed must declare the target Miniflux feed ID. The collector should validate that the feed exists before syncing. It should not guess based on title alone.

- Managed Miniflux feeds should be exclusive to the collector and disabled for normal Miniflux fetching. If Miniflux continues to fetch the old RSSGen feed URL, state ownership becomes ambiguous and duplicate detection becomes less predictable.

- Miniflux cleanup must not remove entries used as watermarks for managed feeds. The deployment should disable automatic read/unread cleanup for these feeds or globally for the instance. If cleanup remains enabled, the no-local-state design can lose its only durable watermark.

- First sync requires an explicit bounded policy: either an `initial_limit` or an `initial_since` cutoff. If no Miniflux watermark exists, the collector should not attempt unlimited historical import.

- If a later sync cannot find the known Miniflux watermark within the configured upstream scan cap, the feed sync should fail rather than import everything it saw. This makes feed deletion, cleanup, URL drift, or upstream ordering changes visible.

- New entries are imported oldest-first after discovery. Upstream scanning can be newest-first for early stop, but writes should be chronological.

- A feed sync should be contiguous. If source list fetching, detail fetching, media archiving, or Miniflux import fails for an entry, the collector should stop the current feed sync and retry later. It should not skip the failed entry and continue with newer ones.

- Afdian sync should stop using local SQLite article storage. It should still fetch list pages newest-first, but only fetch details and comments for candidates newer than the Miniflux watermark.

- Afdian author slug to upstream user ID resolution may still require a lightweight profile lookup. A later optimization may allow configured upstream user IDs, but this spec's main saving is avoiding old list traversal and old detail fetches.

- Afdian detail and comment fetching should default to serial, rate-limited behavior in collector mode. The goal is slow durable collection, not fastest possible backfill.

- Zhihu sync should keep using the current `/api/v3/moments/{user_id}/activities` data source and current content extraction rules: prefer rendered `target.content`, then `target.detail`, then `target.excerpt`. This is based on observed private API behavior and must be treated as unstable.

- Zhihu image handling should be performed after lazy image normalization and before Miniflux import. The media step must process images produced both by raw HTML content and by rendered pin blocks.

- Media archiving should be route-agnostic even if the first required consumer is Zhihu. A normalized article candidate should pass through one HTML media rewrite step before import.

- Local media should be stored in a content-addressed directory using a SHA-256-derived filename and an extension derived from the validated MIME type. Writes should be atomic, and existing files should be reused.

- The media server should expose immutable local URLs under a stable public base URL. The configured public URL must be reachable by the browser that renders Miniflux. If Miniflux is served over HTTPS, the media public URL should also be HTTPS or same-origin to avoid mixed-content blocking.

- The media archiver should allow only image MIME types by default and enforce a maximum object size. Video archiving is not part of this version.

- Miniflux Entry Import has no enclosure field. Image preservation must therefore happen by rewriting HTML `<img>` and related image attributes before import, not by relying on RSS/Atom enclosures.

- Data URI images are not the primary design. Miniflux can display data URI images, but storing base64 image payloads inside entry HTML would bloat PostgreSQL rows, API payloads, and client sync data. Use file-backed local media instead.

- Runtime should expose two collection modes: a long-running daemon that schedules configured feeds with jitter and rate limits, and a `collect-once` mode that runs configured feeds once and exits with a non-zero status on failure.

- Existing health, notification, and status concepts should be reused where possible. Status should move from "feed XML refresh" language to "collector sync" language: scanned, imported, duplicate/watermark hit, media archived, last success, last error, and disabled/failing feeds.

- Synthetic feed provisioning, if needed, should prefer OPML import with disabled feeds. Direct feed creation can require Miniflux to fetch and parse the feed URL immediately, which is not ideal once RSSGen no longer serves feed XML as its primary interface.

## Testing Decisions

- The highest useful test seam is the collector sync for one configured feed. Tests should use fake source adapters, fake media archivers, and fake Miniflux clients to assert externally visible behavior: which upstream pages are scanned, which details are fetched, which entries are imported, in what order, and how failures are reported.

- Collector tests should cover the main watermark path: Miniflux has a latest URL, upstream pages are newest-first, discovery stops at that URL, and only newer entries are imported oldest-first.

- Collector tests should cover first sync with `initial_limit` and/or `initial_since`, proving that a missing watermark does not trigger unbounded import.

- Collector tests should cover watermark-not-found behavior under a scan cap, proving that the sync fails closed instead of flooding Miniflux with old history.

- Collector tests should cover import idempotency behavior: a `201` response counts as newly imported, a `200` response is reported as already existing, and neither path causes the collector to continue after an unexpected error.

- Collector tests should cover stop-on-failure semantics for list fetch failure, detail fetch failure, media archiving failure, and Miniflux import failure.

- Miniflux client tests should use an HTTP test server and assert request method, API prefix, authorization header handling, request body fields, response parsing, and error mapping for non-2xx responses.

- Miniflux client tests should include the Entry Import contract: `url` is required by the caller, `external_id` is sent when available, `status` defaults to `unread`, `201` means created, and `200` means existing.

- Media archiver tests should exercise HTML rewriting as behavior, not implementation details: real image URLs are replaced with local media URLs, already-local media URLs are left alone, unsupported schemes are ignored or rejected consistently, and text content remains intact.

- Media archiver tests should cover MIME validation, max-size rejection, atomic write behavior, content-addressed dedupe, failed downloads, and deterministic public URL generation.

- Zhihu tests should preserve existing behavior around category derivation, stable GUID generation, lazy image normalization, pin block rendering, answer question detail prepending, include filters, and self-interaction filtering.

- Afdian tests should preserve existing behavior around slug feed metadata, `post_id` identity, detail body rendering, comment rendering, image URL capture, upstream business errors, and author alias handling.

- Afdian collector tests should add behavior for "detail calls only happen before the Miniflux watermark" and "comment failures do not erase article content unless the chosen sync policy treats them as hard failures."

- Runtime tests should cover daemon startup wiring, `collect-once` exit codes, disabled route handling, status output shape, and graceful shutdown.

- Existing pipeline, refresher, route, feed generation, store, health, and notifier tests are useful prior art, but the new feature should avoid testing XML generation as the central behavior because XML is no longer the product boundary.

- Live upstream calls to Afdian, Zhihu, or a real Miniflux instance should not be required for normal tests. Use fixtures and HTTP test servers for deterministic behavior.

## Out of Scope

- Supporting non-Miniflux backends.
- Preserving RSS/Atom feed serving as the primary product interface.
- Keeping local SQLite as an article body cache for Afdian.
- Writing images into Miniflux's PostgreSQL schema as first-class blobs.
- Modifying Miniflux source code.
- Adding a separate third-party image host such as PictShare or Zipline in the first implementation.
- Archiving videos or audio resources.
- Building a browser UI for collector management.
- Unlimited historical backfill.
- Solving future Zhihu private API breakage by adding separate article-detail crawling.
- Multi-user Miniflux tenancy beyond the configured API token and feed IDs.

## Further Notes

- Miniflux Entry Import is available from Miniflux 2.2.16. The target instance should be at least that version, with API access enabled.

- The API facts in this spec were checked against the official Miniflux documentation through Context7 on 2026-08-26.

- The current local Miniflux instance on `:8080` was not successfully verified during prior read-only probing because `GET /v1/version` returned HTTP 502 at that moment. Implementation should include a startup capability check that reports version/API availability clearly.

- The migration should be treated as a backend ownership change, not a small route tweak. The critical invariant is: for managed feeds, Miniflux owns durable entries and RSSGen owns collection plus optional local media files.

- Before enabling the collector against existing subscriptions, disable Miniflux's normal fetching for those feeds and ensure cleanup settings will not delete watermark entries.
