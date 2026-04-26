# afdian 路由 list/detail 流水线化设计

**日期**：2026-04-26
**状态**：已确认设计，待实施
**关联**：`RSSGen/routes/afdian.py`、`tests/test_afdian_caching.py`、新增 `tests/test_afdian_pipeline.py`

## 背景与目标

当前 afdian 路由的 `fetch` 严格分两阶段：先 `_get_post_list` 翻完所有列表页（serial），拿到 `posts: list[dict]` 后再循环每个 post 调 `_get_post_detail` 取正文并落到 SQLite store。两阶段之间是硬墙——必须等 list 全翻完，第一篇 detail 才能开始下载。

**目标**：把 list 翻页和 detail 拉取做成流水线——每翻完一页 list，立即派出该页的 detail 任务，让"已下载的内容尽快落到 SQLite store"。

**核心动机**：容错 + 增量持久化。Scraper 的 `_rate_limit_wait` 是全局共享的串行闸门，所以总耗时几乎不变；优化的是**第一篇 detail save 的时间**和**抓取中途失败时的成果保留率**。

**非目标**：
- 不放宽 rate limit、不引入多 scraper 并发——风控压力优先于速度
- 不引入 worker pool / asyncio.Queue 等中间件——YAGNI
- 不改变 list 翻页本身的串行特性——pagination 依赖前页 `publish_sn`，固有顺序

## 关键决策

| 决策 | 选择 | 理由 |
|---|---|---|
| 实现路径 | `_get_post_list` 改 async generator，`fetch` 用 `create_task` 调度 detail | 改动局部、结构清晰、单测易写 |
| 并发模型 | 每个 post 一个 `asyncio.Task`，全局共享 `_rate_limit_wait` 串行 | 总请求量/风控强度不变 |
| detail 顺序 | `gather` 按传入顺序返回结果，`zip(posts, contents)` 装配——feed 顺序与原行为一致 | 不破坏订阅者预期 |
| detail 失败 | best-effort：log warning + 跳过该 item | 跟 SqliteArticleStore "降级"哲学一致 |
| list 翻页失败 | 等已派的 detail task 全跑完（让 save 落地）再 raise | 与"尽快保存已有数据"目标一致 |
| 取消处理 | `CancelledError` 时显式 cancel 所有 pending task | 防止 FastAPI shutdown 时 task 泄漏告警 |

## 架构

### 涉及文件

```
RSSGen/routes/afdian.py     # 重构 fetch + 拆分两个新私有方法
tests/test_afdian_caching.py # 现有 3 个测试预期 0 行改动
tests/test_afdian_pipeline.py # 新增：3 个流水线场景
```

### 数据流（改造后）

```
T=0    : 派 list page 1 请求 → 等 0.5s 限流 → 返回 10 个 post
T=0.5  : 创建 10 个 _fetch_one_content task（排队等限流闸门）
T=0.5  : async for 进入下一轮 → 派 list page 2 请求（同样竞争闸门）
T=1.0  : 第 1 个抢到闸门的请求开始下载（可能是 list page 2，也可能是某个 detail）
...
T=N    : 所有 task 完成 → asyncio.gather 收齐 → zip+filter → 返回 FeedItem 列表
```

跟当前对比：
- 总请求数完全一致
- 总耗时几乎一致（受限于 rate_limit × 总请求数）
- **第一篇 detail save 的时间提前 ≈ list 总页数 × rate_limit**
- 中途失败时已落库的 detail 数量显著增多

## 模块设计

### 1. `_iter_post_list`（替代 `_get_post_list`）

```python
async def _iter_post_list(
    self, scraper: Scraper, user_id: str, author_slug: str,
    per_page: int = 10, limit: int = 0,
) -> AsyncIterator[list[dict]]:
    """逐页 yield post list；保留现有 limit / 翻页终止逻辑。"""
```

行为：
- 内部 `while True` 循环结构跟当前 `_get_post_list` 一样
- 每次拿到 `post_list` 后 `yield post_list`（原本是 `all_posts.extend(post_list)`）
- `limit` 截断：累计 yield 条数到 `limit` 时，最后一页只 yield 前 `limit - 已yield` 条然后 `return`
- 异常依然向上抛（pagination 失败由 `fetch` 处理）

### 2. `_fetch_one_content`（新增）

```python
async def _fetch_one_content(
    self, scraper: Scraper, article_store, post: dict
) -> str:
    """单篇文章内容获取：先查 store，未命中则下 detail 并落库。"""
    post_id = post.get("post_id", "")
    if article_store:
        cached = await article_store.get("afdian", post_id)
        if cached is not None:
            return cached

    content = await self._get_post_detail(scraper, post_id)
    logger.info(f"文章详情下载成功: {post.get('title', post_id)}")
    if article_store and content:
        await article_store.save("afdian", post_id, content)
    return content
```

### 3. `_make_feed_item`（新增）

把当前 `fetch` 内的 enclosures 提取 + FeedItem 构造原样搬过来，让 `fetch` 主循环更短。纯重构。

### 4. `fetch`（重写）

```python
async def fetch(self, article_store=None, **kwargs) -> list[FeedItem]:
    path_params: list[str] = kwargs.get("path_params", [])
    if not path_params:
        raise ValueError("需要指定作者 url_slug，如 /feed/afdian/{author_slug}")
    author_slug = path_params[0]

    limit = int(kwargs.get("limit", 20))
    logger.info(f"开始抓取 {author_slug}，limit={limit}")

    scraper = self._get_scraper()
    user_id = await self._get_author_id(scraper, author_slug)

    posts: list[dict] = []
    tasks: list[asyncio.Task[str]] = []

    try:
        async for page in self._iter_post_list(scraper, user_id, author_slug, limit=limit):
            for post in page:
                posts.append(post)
                tasks.append(asyncio.create_task(
                    self._fetch_one_content(scraper, article_store, post)
                ))
    except asyncio.CancelledError:
        # shutdown 中途 - 取消所有未完成 task，避免泄漏告警
        for t in tasks:
            if not t.done():
                t.cancel()
        raise
    except Exception:
        # list 翻页失败 - 但仍等已派的 detail task 完成（让 save 落地）
        if tasks:
            await asyncio.gather(*tasks, return_exceptions=True)
        raise

    try:
        contents = await asyncio.gather(*tasks, return_exceptions=True)
    except asyncio.CancelledError:
        for t in tasks:
            if not t.done():
                t.cancel()
        raise

    items: list[FeedItem] = []
    for post, content in zip(posts, contents):
        post_id = post.get("post_id", "")
        if isinstance(content, Exception):
            logger.warning(f"文章详情获取失败，跳过 {post_id}: {content}")
            continue
        items.append(self._make_feed_item(post, content))

    logger.info(f"抓取完成 {author_slug}: {len(items)}/{len(posts)} 条文章")
    return items
```

## 错误处理详细

### detail 失败（`_get_post_detail` raise）

异常自然冒到 task → `gather(return_exceptions=True)` 收成 `Exception` 对象 → 主循环识别 → log warning + 跳过。已成功 save 的不受影响。

### list 翻页失败（`_iter_post_list` 内部 raise）

`async for` 中冒出异常 → 进 except 分支 → `await asyncio.gather(*tasks, return_exceptions=True)` 等已派任务完成（让 save 落地）→ raise 原异常 → BackgroundRefresher 重试机制兜底。

### `article_store.save` 失败

不变。SqliteArticleStore 内部已经吞 warning，永不传播。

### `article_store.get` 失败

不变。同上，返回 None 即视为 cache miss，照常调 detail API。

### Cancellation（FastAPI shutdown）

`async for` 阶段或 `gather` 阶段抛 `CancelledError` 时，**两个分支都**遍历所有未完成 task 显式 `cancel()` 后再 raise——避免 `Task was destroyed but it is pending!` 警告。注意 `CancelledError` 在 Python 3.8+ 继承自 `BaseException` 而非 `Exception`，所以不会被 list 翻页失败的 `except Exception` 误捕。

## 日志

| 位置 | 内容 | 级别 | 备注 |
|---|---|---|---|
| `fetch` 起始 | `开始抓取 {slug}，limit={N}` | INFO | 不变 |
| `_iter_post_list` 每页 | `列表页 {page}: 获取 {N} 条，累计 {M} 条` | INFO | 从原 `_get_post_list` 搬来 |
| `_get_post_detail` 内 | `文章详情 {post_id}: {N} 字符` | DEBUG | 不变 |
| `_fetch_one_content` 成功 | `文章详情下载成功: {title or post_id}` | INFO | 从原 `fetch` 搬来 |
| 主循环识别失败 | `文章详情获取失败，跳过 {post_id}: {error}` | WARNING | 新增 |
| `fetch` 结尾 | `抓取完成 {slug}: {成功数}/{总数} 条文章` | INFO | 增加分子分母，方便观察失败比例 |

不加：
- "开始派 N 个 task"——`_fetch_one_content` 自带成功日志足够追踪
- 单篇耗时 / 总耗时统计——YAGNI

## 测试

### 新增 `tests/test_afdian_pipeline.py`

三个核心场景，用 mock + tmp_path 的 SqliteArticleStore：

**场景 1：所有 detail 成功 — 顺序与数量正确**
- mock `_iter_post_list` yield 两页（共 5 个 post）
- mock `_get_post_detail` 按 post_id 返回不同 content（如 `f"<p>{post_id}</p>"`）
- 断言：返回 FeedItem 数 = 5，顺序与 mock 输入一致，每个 content 正确

**场景 2：部分 detail 失败 — 跳过失败、保留成功**
- mock `_get_post_detail`：调用 5 次，其中第 3 次（按 post_id）抛 `RuntimeError("boom")`
- 断言：返回 FeedItem 数 = 4，缺失的恰好是失败那篇
- 断言：其余 4 篇内容已 save 到 store（直接查 store.get）

**场景 3：list 翻页中途失败 — 已派 detail 仍落库**
- mock `_iter_post_list`：第一页正常 yield 3 个 post，第二页 `raise RuntimeError("list boom")`
- 断言：`fetch` 抛 RuntimeError
- 断言：这 3 个 post 的 content 已 save 到 store（验证 §错误处理 的 try/except + gather）

### 改造 `tests/test_afdian_caching.py`

预期**零行改动**仍全绿：
- `test_store_hit_skips_api`：单 post 命中 store，并发后 `_get_post_detail` 仍未被调用
- `test_store_miss_calls_api_and_saves`：单 post 未命中，并发后 content 与落库都成立
- `test_no_store_still_works`：单 post 无 store，并发后正常返回

如果改造后这 3 个测试需要修改才能通过，说明 `fetch` 的对外契约被破坏了——这是回归信号。

### 不写的测试

- 不测时序顺序（"detail 1 必须在 list page 2 之前完成"）—— gather 不保证完成顺序，也不应保证
- 不测真实并发数——单 scraper 全局限流，所谓"并发"是排队，验真实并发数没意义
- 不测 cancellation 路径——FastAPI shutdown 极少触发；真出问题靠日志

## 不在本次范围

- 列表翻页早停优化（"遇到 N 篇连续 store 命中就停止翻页"）——独立 spec
- Scraper 多实例并发 / 限流分级 / 滑动窗口——独立 spec
- 持久化 feed_cache、批量预热接口、手动失效 API——独立 spec

## 升级影响

- 现有订阅者完全无感：feed 内容、顺序、HTTP 接口都不变
- 配置无新增项
- 依赖无新增（`asyncio.create_task / gather` 是标准库）
