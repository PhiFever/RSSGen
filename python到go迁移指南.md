# oh-my-claudecode 编排评估 & RSSGen 的 Python→Go 无人迁移可行性

> 归档日期：2026-06-07
> 主题：评估 oh-my-claudecode（OMC）这个 Claude Code 插件能否胜任"更好的 agent 编排"，落地到一个具体场景——用小米 mimo-v2.5-pro（走 Anthropic 兼容端点）+ 无人工作流，把 RSSGen 从 Python 迁移到 Go（动机是缩小 Docker 镜像）。本质是一次"烧将过期的 token plan + 试模型能力 + 试无人流"的实验。
> 主要出处：
> - OMC 仓库：github.com/Yeachan-Heo/oh-my-claudecode（35.9k★，MIT，活跃维护）
> - 目标项目：github.com/PhiFever/RSSGen（自托管 RSS 生成框架，FastAPI）
> - 关键代码证据：OMC `src/hooks/bridge.ts:2154`、`agents/*.md` frontmatter
> - mimo 接入：`~/.claude/settings-mi.json`（端点 `token-plan-cn.xiaomimimo.com/anthropic`，token 已打码）

---

## 0. 一句话总览

OMC 对这个场景**可用且接入无障碍**，但它宣传的"零学习成本/省 30-50% token"对本场景**收益为零或失效**。真正决定成败的不是编排框架，而是 RSSGen 里两个"红块"（知乎签名 + curl_cffi 反爬）的等价性，以及"纯 Go 库"这条硬约束。

**本次讨论我有两处判断被用户推翻/修正，均有代码证据**（见第 3 节），记录在案以免复用错误结论。

---

## 1. OMC 是什么（事实层面）

- Claude Code 插件 + npm CLI（包名 `oh-my-claude-sisyphus`，命令 `omc`）。在 Claude Code 原生能力上叠一层编排：19 个专职 agent、多种执行模式、HUD 状态栏、技能学习、成本追踪。
- 核心编排面是 **Team**（`team-plan → team-prd → team-exec → team-verify → team-fix` 分阶段流水线），外加 Autopilot / Ralph / Ultrawork / UltraQA / Pipeline 等模式。
- 多模型 provider 写死为 **claude / codex / gemini / grok**，**无 mimo**。
- `omc team` 走 tmux CLI worker（真实进程分屏），`/team` 走 in-session 原生 team——两套运行时，文档里 caveat 极多，"零学习成本"是营销话术。

## 2. mimo 怎么接进来（已确认可行）

用户的 `settings-mi.json` 配置：
- `ANTHROPIC_BASE_URL = https://token-plan-cn.xiaomimimo.com/anthropic`（**Anthropic 兼容端点**）
- `ANTHROPIC_MODEL = mimo-v2.5-pro[1m]`
- `ANTHROPIC_DEFAULT_{HAIKU,SONNET,OPUS}_MODEL` **三档全映射到 mimo-v2.5-pro**（Haiku 档是裸名，Sonnet/Opus 带 `[1m]`）
- `CLAUDE_CODE_EFFORT_LEVEL=max`

即：Claude Code 指向 mimo 后端，OMC 叠在上面当 skill/prompt 脚手架用。

## 3. 我被修正的两点（带证据，重点）

### 3.1 「OMC 的 smart routing 会因认 Anthropic model id 而乱套」——**错，撤回**

我原判断：OMC 的模型路由/成本优化认 Claude 的 model id，配 mimo 会乱。
**用户反驳**：工作流不至于用固定字符串做模型匹配。
**查证结论（用户对）**：

- 所有出厂 agent 的 frontmatter 写 `model: opus` / `model: sonnet` / `model: haiku`（`agents/*.md`），是**档位别名**，不是硬编码 id。Claude Code 解析这些别名正是走 `ANTHROPIC_DEFAULT_*_MODEL` 三个环境变量 → 干净解析到 mimo。
- `src/hooks/bridge.ts:2154` 明确：custom sub-agent 应 pin **tier alias 而非 bare Anthropic ID**；"**Shipped OMC agents already do this**"；当 resolver env vars 配好时，enforcer 会**主动 deny** 写死 `claude-xxx` 的调用并给出 tier-alias 指引。
- 硬编码的 `claude-*` 字符串只出现在 `think-mode` / `thinking-block-validator` 两个 hook（与路由无关）。

**改变判断的就是上述代码。** 修正后剩下的弱提醒：用户把三档全压到同一个 mimo，所以"Haiku/Opus 分级省 token"的**收益为零**（只有一个底层模型），但功能不坏，对"烧 flat token plan"无所谓。另外 `think-mode` 给模型名加 `-high` 后缀的逻辑对非 claude 模型大概率匹配不上 → 静默 no-op，无害退化。

### 3.2 「curl_cffi 在 Go 下是难点/无 oracle」——**过度悲观，部分撤回**

我原判断：curl_cffi 的 TLS 指纹模拟是"🔴 真陷阱"。
**用户反驳**：Go 下没有对应库吗？
**修正**：有近乎直接的对应库——

- **`github.com/bogdanfinn/tls-client`**：高层封装，具名浏览器画像（`chrome_131` 等），对应 curl_cffi 的 `impersonate="chrome131"`，**纯 Go**（基于 utls fork），对小镜像友好。最接近的替代。
- 底层 `refraction-networking/utls`；另有 `Danny-Dasilva/CycleTLS`。
- cgo 版 curl-impersonate C 绑定存在但**会撑大镜像/毁静态编译，别用**。

**仍然成立的窄结论**：curl_cffi 的 `chrome131` 与 tls-client 的 `chrome_131` 是两套独立重实现，JA3/JA4/HTTP2 指纹可能有细微差异，知乎反爬接不接受是**经验问题、无离线单测 oracle**——只能真打知乎接口验证。难点不在"没库"，在"指纹够不够骗过知乎"是运行时验收点。

---

## 4. RSSGen 迁移难度图（具体到文件）

项目很小：**生产代码 ~2148 行，测试 ~1969 行**，测试覆盖扎实——这是迁移最值钱的资产（等价性 oracle）。难度呈双峰：

| 模块 | Python | Go 对应 | 难度 |
|---|---|---|---|
| Web 框架/路由 | `app.py` `routes/` (FastAPI/uvicorn) | chi / gin / net-http | 🟢 机械翻译 |
| feed 生成 | `feed.py` (feedgen) | gorilla/feeds | 🟡 输出格式细微差异 |
| SQLite 存储 | `article_store.py` (aiosqlite) | **modernc.org/sqlite（纯 Go）** | 🟡 见第 5 节坑 |
| HTML 清洗 | `feed.py` (nh3 / Rust ammonia) | bluemonday | 🟡 清洗规则逐条对齐 |
| **反爬 HTTP 客户端** | `core/scraper.py` (curl_cffi 模拟 chrome131 TLS 指纹) | **bogdanfinn/tls-client** | 🔴 指纹保真度=运行时验收点 |
| **知乎签名** | `sign/zhihu/sign.py`（680 行 SM4+MD5+自定义 base64 打乱） | gmsm/SM4 + 手写 | 🔴 必须逐字节一致 |

- **知乎签名**：有 `tests/test_zhihu_sign.py` 的 golden 向量 → 把期望值原样搬到 Go 测试，无人循环就有真 oracle，适合自动磨。
- **TLS 指纹**：无离线 oracle，只能真打知乎确认。

## 5. 与"小镜像"初衷强相关的坑（必须写进 prompt）

迁移全部动机是镜像体积。Go 能否真做出 scratch/distroless ~20MB 镜像，**取决于选库**：

- ✅ `modernc.org/sqlite`（纯 Go）+ `bogdanfinn/tls-client`（纯 Go）+ bluemonday → 可静态编译，scratch ~15-25MB，完胜 Python slim（150-400MB）。
- ❌ 一旦混进 `mattn/go-sqlite3`（cgo）或 cgo 版 curl-impersonate → 需 libc、无法 scratch、静态编译废掉，初衷落空。

**无人流默认不知道要优先纯 Go 库**——这条硬约束必须显式写进 prompt。

## 6. OMC 是不是最优载体？

- 对 2k 行、测试齐全的小项目，OMC 多 agent 重型编排**过度配置**。
- RSSGen 仓库**自己就在用 superpowers**（有 `docs/superpowers/specs` `plans`、CLAUDE.md）——原生"写计划 → TDD → verify 循环"更透明可调试，且正是项目作者已用的范式。
- OMC 相对原生的增量价值主要是开箱即用的"不验证通过不罢休"循环；但 mimo 后端下 Claude 专属卖点失效，增量进一步缩水。
- 用户目标是低风险实验（试 OMC 编排本身 + 试无人流 + 烧 token），失败本身有观测价值 → 不劝退，但应设计成可学到东西的实验。

## 7. 实验落地建议（下一步待办）

1. **别一把 autopilot 梭哈**，分两段：红块（sign + scraper）单独用"port golden 测试 → verify/fix 循环"专攻；绿/黄块批量推。
2. **硬约束写进 prompt**：纯 Go SQLite、纯 Go TLS、最终 scratch 多阶段构建。
3. **两个可量化成功判据**：① `go test` 全绿（尤其 sign 的 golden 向量）② 最终镜像体积 < 阈值。给无人循环一个客观停止条件。
4. （待办）把上述落成一份正式迁移计划文档：模块拓扑顺序、等价性测试搭法、纯 Go 约束清单、双验收门禁。

---

## 8. 诚实标注 / 未验证项

- OMC 的"省 30-50% token" benchmark 方法学**未逐一核对**（repo 有 `benchmark/` `benchmarks/` 目录），当营销数字看待；何况本场景三档同模型，省钱卖点本就失效。
- `think-mode` 对 mimo 的 `-high` 后缀行为是**推断**为静默 no-op，未实跑确认。
- mimo-v2.5-pro 经 `token-plan-cn.xiaomimimo.com/anthropic` 的 Anthropic 兼容度、对 sub-agent/tool-use/`[1m]` 长上下文的实际表现**未实测**——这正是用户想测的"模型能力"本身。
- RSSGen 各模块 LOC 来自 `wc -l` 快速统计；知乎签名 680 行的逐行可移植性、curl_cffi `chrome131` 与 tls-client `chrome_131` 的指纹差异**均未实测**。
- Go 库推荐（tls-client / modernc sqlite / bluemonday / gorilla-feeds）基于既有认知，落地时应核对各库当前版本与 RSSGen 具体用法的契合度。
