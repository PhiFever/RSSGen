---
name: rss-image-archiving
description: 为 RSSGen 生成的未来 feed 条目异步归档可信 CDN 图片，并通过 Miniflux 内置媒体代理提供访问
type: project
status: ready-for-agent
date: 2026-08-30
---

# RSS 图片持久归档

## Problem Statement

RSSGen 当前生成 RSS/Atom XML，由 Miniflux 轮询并保存条目。文章正文虽然进入了 Miniflux，其中的图片仍然引用知乎等站点的外部 CDN。只要源图片过期、被防盗链拦截或 CDN 地址失效，已经保存的历史文章也会失去图片。

Miniflux 内置 media proxy 可以代替浏览器即时抓取图片，但它不是持久媒体存储，不能保证源 CDN 失效后的长期可读性。已弃用的 Miniflux-only collector 提案曾包含自行下载图片的方向；本规格只保留并重新设计这项能力，不采用该提案的采集器迁移、直接导入或 Miniflux 单后端架构。

用户希望 RSSGen 在正常生成 feed 时，将可信 CDN 的图片地址立即改写为稳定的内部媒体地址，并异步完成持久下载。图片在 feed 首次被访问时可以暂时尚未就绪，但下载任务必须已经可靠持久化，最终成功的文件必须在 RSSGen 重启或容器替换后继续存在。

## Solution

RSSGen 保持现有产品边界：route 抓取上游内容，pipeline 生成并缓存 RSS/Atom，Miniflux 继续轮询 RSSGen。新增一个可选的媒体归档子系统，在最终 HTML 已完成安全清洗后扫描 `<img src>`。只有命中可信 CDN 规则的 HTTPS 地址才参与归档；其他地址保持原样。

对每个符合条件的图片 URL，RSSGen 以规范化 URL 的 SHA-256 作为稳定身份，先确保一个 Asynq 下载任务已经持久写入 Redis，再把 HTML 中的地址改写为 RSSGen 的内部 `/media/v1/...` 地址。整份 feed 的所有媒体任务都完成持久化确认后，pipeline 才能发布新的 XML 缓存。下载在后台执行，因此刚发布的内部地址可以短暂返回未就绪，但不会出现“feed 已经引用内部地址、系统却从未记录下载任务”的状态。

下载 worker 使用发现该图片的 route 的代理和客户端指纹，但使用独立会话，不继承 Cookie、Authorization 或 route 的额外请求头。文件先流式写入目标目录内的临时文件，校验类型与大小后原子重命名为最终文件。相同规范化 URL 以首次成功下载的内容为准，此后不覆盖。

RSSGen 只在 Pod 内提供内部媒体 URL。Miniflux 使用稳定私钥签名的内置 media proxy 把这些地址安全地提供给浏览器和各类 Miniflux 客户端。Redis/Asynq 负责任务持久化、延迟重试和崩溃恢复；RSSGen 不再实现一套自有任务调度器。

## User Stories

1. As an RSSGen operator, I want images referenced by newly generated feeds to be archived locally, so that old Miniflux entries do not depend indefinitely on the source CDN.
2. As an RSSGen operator, I want RSSGen to keep serving RSS and Atom normally, so that media archiving does not revive the abandoned Miniflux-only collector migration.
3. As a Miniflux user, I want archived images to render in every client that can access my Miniflux instance, so that clients do not need direct access to the RSSGen Pod network.
4. As an RSSGen operator, I want image URLs to become stable as soon as a feed is generated, so that later completion of the asynchronous download does not require changing entry HTML.
5. As an RSSGen operator, I want every rewritten image to have a durable task before new feed XML is published, so that an internal URL is never published without a recorded path to completion.
6. As an RSSGen operator, I want a Redis enqueue failure to leave the old feed cache in place, so that a degraded queue cannot silently publish incomplete archival work.
7. As an RSSGen operator, I want downloads to run asynchronously with bounded concurrency, so that feed generation does not wait for all image bytes to arrive.
8. As an RSSGen operator, I want image tasks to survive RSSGen restarts, so that a process restart does not discard pending archival work.
9. As an RSSGen operator, I want transient download failures to retry a small, bounded number of times, so that short outages recover without creating months of pointless retry traffic.
10. As an RSSGen operator, I want exhausted downloads to be reported through the existing notification system, so that permanently broken images are visible.
11. As an RSSGen operator, I want normal feed regeneration to retry an old exhausted image after a cooldown, so that recovery does not require a media-specific CLI or manual scheduler.
12. As an RSSGen operator, I want repeated discovery of the same URL to reuse one file and one active task, so that multiple feeds do not duplicate work.
13. As an RSSGen operator, I want the first successfully archived bytes for a URL to remain immutable, so that historical entries do not change unexpectedly.
14. As an RSSGen operator, I want only explicitly trusted CDN host patterns to be archived, so that the media worker is not an arbitrary URL downloader.
15. As an RSSGen operator, I want wildcard CDN rules to match DNS labels safely, so that I can cover evolving CDN subdomains without maintaining a long exact-host list.
16. As an RSSGen operator, I want redirects and resolved IP addresses to be revalidated, so that an allowed hostname cannot redirect or resolve to private infrastructure.
17. As an RSSGen operator, I want unsafe formats and oversized objects rejected before publication as files, so that the media store remains bounded and browser-safe.
18. As an RSSGen operator, I want downloads to inherit the route proxy, so that image acquisition follows the same network path as the scraper that discovered it.
19. As an RSSGen operator, I want media downloads isolated from route credentials, so that CDN requests cannot receive article-site cookies or authorization headers.
20. As an RSSGen operator, I want temporary files and final files to share a filesystem, so that successful publication uses an atomic rename.
21. As an RSSGen operator, I want already archived files to remain servable even when Redis is intentionally not configured, so that queue removal does not make the archive unreadable.
22. As an RSSGen operator, I want an installation without Redis to keep original CDN URLs, so that media archiving remains an opt-in feature.
23. As an RSSGen operator, I want active-mode configuration errors to fail startup, so that a partially configured archive cannot silently change URL policy.
24. As a Miniflux user, I want pending images to fail clearly and later become available at the same URL, so that an early request cannot poison the permanent media identity.
25. As an RSSGen operator, I want ready files to support normal image GET, HEAD, and range requests, so that Miniflux and browser clients can load them efficiently.
26. As an RSSGen operator, I want media status included in `/status`, so that queue degradation and failed downloads can be diagnosed without inspecting Redis directly.
27. As an RSSGen operator, I want sensitive URL queries and proxy credentials omitted from logs, so that operational diagnostics do not leak secrets.
28. As an RSSGen operator, I want archived files and Redis state stored on persistent volumes, so that container replacement does not erase completed or queued work.
29. As an RSSGen operator, I want RSSGen to retain old cached feeds and serve completed media during a temporary Redis outage, so that queue downtime does not become a total reader outage.
30. As an RSSGen operator, I want the default Compose deployment to include a private Redis service without a host-published port, so that enabling the feature does not require designing another scheduler deployment.

## Implementation Decisions

- This specification supersedes only the media-archiving portion of the abandoned Miniflux-only collector design. RSSGen remains an RSS/Atom generator, Miniflux remains a polling consumer, and direct Miniflux entry import is unchanged.

- The feature applies only to future feed generations. It does not scan or rewrite historical entries already stored in Miniflux.

- All RSSGen routes use the same route-agnostic media pipeline. A route need not implement image downloading itself.

- The pipeline remains the orchestration boundary. The order is: route fetch, final HTML sanitization, eligible image discovery and rewrite, durable task assurance, RSS/Atom serialization, then feed-cache publication.

- Only `src` on `<img>` elements surviving final HTML sanitization is considered. CSS URLs, `srcset`, enclosures, videos, audio, and attributes removed by sanitization are not processed.

- URLs not covered by an allowed-host rule remain unchanged and do not create tasks. Active media mode therefore affects only explicitly trusted CDN traffic.

- Source URLs must use HTTPS and the default HTTPS port. Host matching is case-insensitive and occurs at DNS label boundaries. A rule such as `*.zhimg.com` matches subdomains at any depth, but not the apex `zhimg.com` and not lookalikes such as `evilzhimg.com`; the apex can be listed separately.

- Every redirect is treated as a new request target. At most three redirects are followed, and every hop must independently pass scheme, port, host-pattern, DNS, and address-class validation.

- DNS results in loopback, private, link-local, multicast, unspecified, or other non-public ranges are rejected. Validation must be applied to the address actually used for the connection, not merely to the URL string.

- Media identity is the lowercase hexadecimal SHA-256 of a conservatively normalized original URL. Normalization lowercases scheme and host, removes the fragment and default `:443`, and preserves path bytes, query bytes, parameter order, and parameter duplication.

- Media identity is URL-addressed rather than content-addressed. Different URLs may store duplicate bytes. For a given normalized URL, the first successful download wins and the final file is never overwritten.

- Internal URLs and disk layout use the versioned, sharded form `/media/v1/{first-two-hash-characters}/{64-character-lowercase-hash}`. Requests must match this grammar exactly; arbitrary paths and traversal syntax are rejected.

- A ready media response supports GET, HEAD, and one byte range. It returns the detected safe MIME type, `X-Content-Type-Options: nosniff`, an ETag, and immutable cache headers. Unknown hashes return 404. A registered but not-yet-ready or failed-and-waiting hash returns 503 with `Retry-After`.

- Eligible images are rewritten immediately after durable task assurance, not after download completion. A client may therefore observe a temporary image failure while the archive is being populated.

- If any eligible image cannot be durably assured in Redis, the current pipeline refresh fails and must not replace the existing feed cache. Tasks already assured for other images may remain because they can be shared by other feeds and are useful on the next refresh.

- Redis is used through Asynq. The worker runs inside the RSSGen `server` process; Redis is a separate container in the same deployment. Successful tasks are removed from Redis, while final-file existence is the durable success fact.

- The task ID is the URL hash. An existing pending, active, scheduled, retry, or otherwise live task with that ID satisfies task assurance and prevents duplicate work.

- A task payload contains the original URL, article URL for `Referer`, discovering route name, and feed identity for diagnostics. It does not contain the route proxy URL or proxy credentials. The worker resolves the current route proxy and client fingerprint from RSSGen configuration when executing the task.

- If different routes discover the same URL, file identity remains global. An active task keeps the route context from the first successful enqueue for that cycle. A later retry cycle may use the route that rediscovers the URL.

- The downloader uses a media-specific `tls-client` session configured with the resolved route proxy and impersonation profile. It sends a fixed media user agent, the article URL as `Referer`, and `Accept-Encoding: identity`. It does not share the scraper Cookie Jar or copy Cookie, Authorization, or arbitrary route headers.

- Global worker concurrency defaults to two. Concurrency for any one CDN hostname is one, with an independent media rate limit of at most one request start per hostname per second. Media limits do not consume the article scraper's limiter.

- Connect/TLS establishment is limited to 10 seconds, response headers to 15 seconds, and a complete task to 60 seconds.

- Each task has one initial attempt plus three retries, delayed by 1 minute, 5 minutes, and 30 minutes. Proxy/network interruption, HTTP 408/425/429, and upstream 5xx are transient. Invalid URL or proxy configuration, disallowed redirect, HTTP 401/403/404/407/410, unsafe MIME, and oversize content fail the current retry cycle immediately.

- Exhausted tasks use Asynq's archive rather than a second custom failed-task database. When a normal feed regeneration rediscovers an archived task after a 24-hour cooldown, RSSGen starts a fresh task cycle with the same URL identity. This is the only automatic reactivation mechanism; there is no media-specific timer or CLI.

- The existing notifier gains a media-failure event rather than pretending the failure is a feed HTTP error. It notifies on the first exhausted cycle for a URL, remains silent for repeated automatic cycles until success, and becomes eligible to notify again only after that URL has succeeded. All cycles remain visible in logs and status.

- Original URLs and Referer values are stored in private Redis/AOF because the worker requires them. Application-level encryption is not added. Logs and notifications must redact sensitive query values and must never print proxy credentials.

- Downloads are streamed through a `maximum-size + 1` limiter. The default maximum object size is 20 MiB and is configurable.

- JPEG, PNG, GIF, WebP, and AVIF are accepted. SVG and every other type are rejected. Magic-byte detection is authoritative; a conflicting HTTP `Content-Type` is logged as a redacted warning, and the detected safe type is used when serving.

- A worker creates a mode-0600 temporary file in the final shard directory. After the stream, size, and type validations succeed, it flushes and atomically renames the file to the final path. Startup removes only temporary files owned by this subsystem; it never deletes final media automatically.

- Configuration has three modes:
  - Without a media storage directory and without Redis, media archiving is disabled and newly generated feeds retain origin CDN URLs.
  - With a media storage directory but without Redis, RSSGen serves existing media files but does not enqueue, download, or rewrite newly generated images.
  - With a media storage directory and Redis, media archiving and URL rewriting are active.

- Configuring Redis without media storage is invalid. Active mode also requires an absolute internal base URL and at least one allowed-host rule. Configured storage must exist or be creatable, writable, and capable of same-filesystem atomic rename. Configured Redis must be reachable during startup. Validation failures stop startup.

- The media configuration contains the storage directory, internal base URL, allowed-host patterns, maximum object size, worker/rate limits, and Redis address/database. A Redis password is supplied through `REDIS_PASSWORD`, not committed to YAML.

- The intended internal base URL is an explicitly configured Pod-network address such as `http://rssgen:8000`; it is not inferred from an incoming request. Raw feed XML is therefore deployment-specific and is not expected to render archived images in readers outside this Miniflux deployment.

- Miniflux uses its built-in signed proxy with `MEDIA_PROXY_MODE=http-only`, `MEDIA_PROXY_RESOURCE_TYPES=image`, and a stable `MEDIA_PROXY_PRIVATE_KEY`. `FETCHER_ALLOW_PRIVATE_NETWORKS=1` remains enabled so Miniflux can fetch the RSSGen Pod URL. Miniflux proxying is an access layer, not the durable store.

- The default deployment adds a Redis container with no published host port, a persistent data volume, AOF enabled, and `appendfsync always`. Media files use a separate persistent volume mounted into RSSGen.

- If Redis is configured but unavailable at startup, RSSGen fails startup. If it becomes unavailable at runtime, RSSGen continues serving old feed cache entries and ready media; new media-enabled refreshes fail rather than silently reverting to CDN URLs. `/status` reports the degraded condition.

- Existing route refresh and HTTP cache-miss generation are the only feed-generation triggers. Both pass through the same pipeline and therefore perform the same media assurance. No separate media scan schedule is introduced.

- Graceful shutdown stops feed refresh production, allows active HTTP work and media tasks a bounded drain period, and leaves unfinished tasks in Redis for the next process. Exact goroutine ordering and short Redis coordination are implementation details rather than new product-level scheduling concepts.

## Testing Decisions

- The primary and highest test seam is one pipeline refresh. Existing pipeline tests are the prior art: register a fake route, provide a controlled media service, and observe returned XML plus the feed cache. Tests should assert external state transitions, not calls between internal helper functions.

- Pipeline tests cover Atom and RSS output with allowed images: final sanitized `<img src>` values become deterministic internal URLs, durable assurance occurs before cache publication, and unrelated HTML remains intact.

- Pipeline tests prove that disallowed hosts, non-HTTPS URLs, removed HTML attributes, `srcset`, and enclosures do not create media work and retain their specified behavior.

- Pipeline tests cover all three configuration modes: disabled mode preserves origin URLs; serve-only mode preserves origin URLs while existing media remains readable; active mode rewrites and assures tasks.

- Pipeline tests cover enqueue failure and timeout. The refresh returns an error, no new XML replaces the old cached value, and already assured shared tasks need not be rolled back.

- Pipeline tests cover idempotency: a ready file avoids enqueue; an existing live task counts as assured; repeated occurrences and multiple feed generations keep the same internal URL and do not create duplicate active tasks.

- Focused downloader contract tests use a controlled transport or HTTP server and assert public behavior: allowed download success, redirect limit, per-hop host/address validation, route proxy selection, credential isolation, timeout classification, bounded streaming, accepted magic bytes, header mismatch handling, and rejection of SVG or oversized data.

- Filesystem tests use a temporary directory and assert mode-0600 temporary creation, cleanup after failure, atomic final publication, first-success immutability, shard layout, and startup cleanup limited to owned temporary files.

- Media HTTP handler tests assert strict path grammar, 404 for unknown identity, 503 plus `Retry-After` for registered non-ready identity, correct MIME and security/cache headers for ready files, HEAD behavior, one valid byte range, and rejection of invalid or multiple ranges.

- Task-handler tests call the handler as a normal Go component with controlled downloader and storage dependencies. They cover initial attempt plus three retries, deterministic skip-retry failures, success removal, archived cooldown reactivation, route-context resolution, and notification suppression across repeated failed cycles.

- Configuration tests follow existing YAML-loading tests. They cover defaults, the three modes, environment-sourced Redis password, wildcard validation, and startup rejection of incomplete active configuration.

- Runtime and router tests follow existing main/refresher/notifier tests. They cover media route wiring, status output, startup validation, runtime Redis degradation, notification formatting, and graceful shutdown without losing a running task.

- Normal tests must not contact real CDNs, Miniflux, proxies, or a shared Redis instance. Queue behavior that specifically depends on Asynq/Redis may use an isolated integration Redis in CI or a Compose smoke test, while business behavior remains testable through the pipeline and task-handler seams.

- Deployment smoke testing verifies the complete path: Miniflux polls a generated feed, its signed proxy requests the internal RSSGen media URL, a pending image becomes ready without changing the feed URL, and the image remains available after restarting RSSGen and Redis.

## Out of Scope

- Replacing RSS/Atom polling with a Miniflux-only collector or daemon.
- Directly importing new route entries into Miniflux.
- Rewriting or backfilling historical Miniflux entries.
- Archiving arbitrary Internet URLs outside configured trusted CDN host patterns.
- Archiving CSS backgrounds, `srcset`, RSS/Atom enclosures, video, audio, SVG, or data URIs.
- Content-based deduplication across different source URLs.
- Overwriting, refreshing, expiring, or garbage-collecting a successfully archived file.
- A public standalone image-hosting API or direct browser access to the RSSGen Pod URL.
- Sharing route cookies, authorization headers, or authenticated scraper sessions with CDN downloads.
- Building a custom task scheduler, media management UI, manual retry CLI, or separate failed-task database.
- Designing online backup, snapshot orchestration, or restore automation. Operators may stop the deployment and copy persistent files/volumes externally.
- Publishing Redis outside the deployment network.
- Guaranteeing that raw RSSGen feed URLs remain portable to readers other than the paired Miniflux deployment when media archiving is active.

## Further Notes

- The central invariant is: **RSSGen must not publish a newly rewritten internal image URL until the corresponding final file exists or a durable Asynq task/state for that URL has been confirmed.** Download completion may be asynchronous; task existence may not be.

- A second invariant is: **after the first successful atomic rename, the media URL is immutable.** Queue state can disappear or be rebuilt without changing the completed file or its URL.

- This design intentionally accepts a brief 503 window after feed publication. It optimizes for durable archival rather than guaranteeing that the first immediate reader request always succeeds.

- The current notifier implementation supports Feishu Incoming Webhook. The media event should reuse that delivery mechanism while using media-specific wording and redacted identifiers.

- The API and behavior assumptions for Miniflux media proxy, Redis persistence, Asynq task identity/retry/archive behavior, and `tls-client` proxy support were checked against their current documentation during the design discussion on 2026-08-30.

- No issue was created for this specification. It is archived locally under `docs/` as requested.
