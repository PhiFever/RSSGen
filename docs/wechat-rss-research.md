# 微信公众号 RSS 化方案调研

> 2026-05-06 | 当前方向：**MMTLS 逆向**（手机端自动化，无需人工扫码）

## 1. 背景与实验结论

### 1.1 手机端 profile_ext 实验（2026-05-05）

通过 PC 微信 + Fiddler 抓包获取 `mp/profile_ext` 的凭证参数（`key`、`uin`、`pass_ticket`、`wap_sid2`），验证这些凭证能否用于自动化请求。

**结论：不可行。** 经 24h 监控验证：

- `key` 实际有效期约 **30 分钟**（20:50 生成 → 21:28 失效）
- key 失效时 `action=home` 直接触发**验证码**
- `wap_sid2` 虽然可通过 `action=home` 的 Set-Cookie 刷新，但依附于 key 的有效性
- key 由微信原生层通过 MMTLS 发放，HTTP 层只能消费、无法续期

**但这也指明了方向**：如果能自行实现 MMTLS 客户端直接向微信服务端请求新 key，就能彻底绕开人工抓包。

### 1.2 key 存活时间实测

监控脚本 `test_key_lifetime.py` 运行结果（2026-05-05 21:18）：

```
[21:18:00] --- 第 1 次检查 ---
[21:18:04] ✅ getmsg 正常
[21:28:04] --- 第 2 次检查 ---
[21:28:08] ❌ getmsg 失败 (ret=-3, errmsg=no session)
[21:28:08] 📌 首次失效！已存活: 0:10:07
[21:28:08] 尝试 action=home 刷新凭证...
[21:28:14] 🚨 触发验证码！立即停止以保护账号。
```

- key 首次获取后约 30 分钟即失效
- 失效后请求 `action=home` 直接触发验证码
- 全链路无 HTTP 层恢复手段

### 1.3 手机端 API 接口（供逆向时参考）

<details>
<summary>展开：profile_ext API 详情</summary>

**文章列表（首页 HTML）**
```
GET https://mp.weixin.qq.com/mp/profile_ext?action=home
    &__biz=Mzg5NDY4Nzk1MQ==
    &uin=MzEzNTIzNjkwMg==
    &key=<32位十六进制>
    &devicetype=UnifiedPCWindows&version=f254191e&lang=zh_CN
    &a8scene=1&session_us=&acctmode=0
    &pass_ticket=<url_encoded>
```
返回 HTML，内含 `var msgList = '...'` JSON（首屏文章）。响应 Set-Cookie 返回新的 `wap_sid2`。

**文章列表（翻页 JSON）**
```
GET https://mp.weixin.qq.com/mp/profile_ext?action=getmsg
    &__biz=Mzg5NDY4Nzk1MQ==
    &f=json&offset=0&count=10&is_ok=1&scene=126
    &uin=MzEzNTIzNjkwMg==
    &key=<同home>&pass_ticket=<同home>
    &wxtoken=&appmsg_token=<从action=home页面提取>
    &x5=0
```
`uin`/`key` 必须配套使用。返回 `{"general_msg_list": "{...}", "can_msg_continue": 1, "next_offset": 10}`。

**文章正文**
```
GET https://mp.weixin.qq.com/s?__biz=...&mid=...&idx=...&sn=...
```
不需要任何凭证，但可能触发验证码。

**凭证刷新链路**
```
微信原生层(MMTLS) 获取 key → 注入 WebView URL 参数
    ↓
action=home → 服务器 Set-Cookie: 新 wap_sid2 + 新 pass_ticket
    ↓          页面内 window.appmsg_token 有新值
action=getmsg → 用刷新后的凭证请求文章列表
```

| 参数 | 来源 | 刷新方式 | 实际有效期 |
|------|------|---------|-----------|
| `uin` | URL 参数 | 不可刷新 | 长期不变 |
| `key` | URL 参数 | **不可通过 HTTP 刷新** | **≈30 分钟** |
| `pass_ticket` | URL + Cookie | Cookie 版本可通过 Set-Cookie 刷新 | 与 key 相同 |
| `wap_sid2` | Cookie | `action=home` 的 Set-Cookie 返回新值 | 随 key 一同失效 |
| `appmsg_token` | Cookie + 页面 JS | `action=home` 页面中有新值 | 随 key 一同失效 |

</details>

## 2. 可行方案总览

| 方案 | 原理 | 凭证有效期 | 自动化程度 | 实现难度 | 当前状态 |
|------|------|-----------|-----------|---------|---------|
| **MMTLS 逆向** | 实现 MMTLS 客户端，自动获取 key | 实时获取 | **全自动** | 高 | **主攻方向** |
| 公众号后台 API | 扫码登录 mp.weixin.qq.com 后台 | ~4 天 | 半自动（需定期扫码） | 低 | 备选 |
| 微信读书 API | 扫码登录微信读书 | 2-3 天 | 半自动 | 低 | 备选 |
| 微信本地数据库 | 读取 PC 微信加密数据库 | 永久（本地） | 全自动 | 中 | 仅能获取新推送 |
| ~~手机端抓包~~ | PC 微信 Fiddler 抓包 | ~30 分钟 | 手动 | - | ❌ 已否决 |

## 3. 主攻方向：MMTLS 逆向

### 3.1 动机

与后台 API 方案相比：

- 后台 API 方案已有多个成熟开源项目（wechat-download-api、WeRSS），直接 clone 即可用，没有增量价值
- MMTLS 方案一旦跑通，是**目前唯一能实现全自动、无需任何人工干预**的路线
- 目前尚无开源的 Python MMTLS 客户端实现，具有独创性

### 3.2 协议架构

```
┌─────────────────────────────────────────────────────────┐
│ 业务层 (mars)    Protobuf API 请求/响应                  │
│                  + AES-CBC 业务层加密                     │
│                  ❌ 未攻克 —— 核心卡点                     │
├─────────────────────────────────────────────────────────┤
│ MMTLS 层         基于 TLS 1.3 修改                       │
│                  Handshake (ECDHE/PSK) → AES-GCM         │
│                  ✅ pymmtls (Python) 已实现                │
├─────────────────────────────────────────────────────────┤
│ TCP 连接         标准 TCP socket                         │
│                  ✅ 标准库                                │
└─────────────────────────────────────────────────────────┘
```

### 3.3 已有轮子

| 轮子 | 覆盖内容 | 状态 |
|------|---------|------|
| [pymmtls](https://github.com/ljc545w/pymmtls) | Python MMTLS 握手 + Record 层加解密 + 本地长连接服务器 | ✅ 可直接用 |
| [Citizen Lab 工具链](https://github.com/citizenlab/wechat-security-report) | Frida 脚本（注入微信导出密钥）+ PCAP 解密 + Protobuf 解码 | ✅ 可直接用 |
| [Citizen Lab 文档](https://github.com/citizenlab/wechat-security-report/tree/main/docs) | MMTLS 格式伪代码 + 请求发送流程 + 业务层加密笔记 | ✅ 参考 |
| 服务端公钥 | 硬编码在客户端中，已提取 | ✅ 已知 |
| [anonymous5l/mmtls](https://github.com/anonymous5l/mmtls) | Go MMTLS 握手（实验性） | 参考 |
| [ljc545w/mmtls](https://github.com/ljc545w/mmtls) | C++ MMTLS 握手（跨平台） | 参考 |

### 3.4 核心卡点：Mars 业务层

mars 是腾讯的跨平台网络基础设施（C++），微信特定部分 (`mars-wechat`) 包含 MMTLS 实现和业务 API 定义。需要搞清楚三个问题：

1. **获取公众号 `key` 的 API 是什么？** — 哪个 Protobuf RPC 能拿到 profile 页的 key
2. **请求/响应的 Protobuf schema** — 消息中有哪些字段、什么类型
3. **用户认证在业务层如何表达** — uin、设备 ID、登录 session 如何编码到 Protobuf

这三个问题互相耦合——**只要抓一次完整的解密流量，就能同时回答**。

### 3.5 流量捕获方案

核心思路：让微信客户端在加载公众号 profile 页面时，用 Frida 导出 MMTLS 密钥 → 同时抓包 → 用 Citizen Lab 工具解密 → 得到明文 Protobuf。

**方案 A：Android 模拟器（推荐，Citizen Lab 已验证）**

在 Windows 宿主机上：

```
1. 安装 Android Studio → 创建 Pixel 5 API 32 模拟器（>=5GB 存储）
2. 下载 WeChat 8.0.49 arm64 APK → adb install
3. 安装 Frida：pip install frida-tools，推送 frida-server 到模拟器
4. git clone https://github.com/citizenlab/wechat-security-report
5. 登录微信 → 运行 Frida 脚本导出密钥
6. 同时 tcpdump 抓包
7. 打开一个公众号的 profile 页面（触发 key 获取流程）
8. 用 decrypt-keylog.py 解密抓包 → 得到明文 Protobuf
```

已验证版本：WeChat 8.0.49 (Android) + Frida + tcpdump。Citizen Lab 脚本位于 `code/frida-scripts/`，钩子函数 `Java_com_tencent_mm_protocal_MMProtocalJni_packHybridEcdh` 和 `unpack`。

**方案 B：Windows PC 微信（需额外适配）**

现有环境已有 PC 微信 + Fiddler 抓 HTTP 层。但 MMTLS 层需要 hook 原生 DLL（`libwechatmm.dll` / `WeChatWin.dll`），Citizen Lab 的 Frida 脚本针对 Android `.so`，需要适配：
- Frida 支持 Windows，可 attach 到 WeChat.exe
- 需找到 Windows 版 MMTLS 导出函数名（不同于 Android JNI 函数名）
- 用 `frida-trace` 扫描 WeChat.exe 的 MMTLS 相关函数

**推荐先走方案 A**——有现成脚本和文档，踩坑最少。

### 3.6 拿到流量后的实现计划

| 阶段 | 内容 | 负责 | 预估耗时 |
|------|------|------|---------|
| 流量捕获 | Android 模拟器 + Frida + 抓包 | 用户 | 1-2 天 |
| Protobuf 逆向 | `protoc --decode_raw` 反推消息结构，定位 key 获取 API | AI | 1-2 天 |
| MMTLS 客户端 | 基于 pymmtls + 逆向出的 schema，实现自动获取 key | AI | 2-3 天 |
| RSSGen 集成 | 编写微信路由，自动获取 key → profile_ext → 文章列表 | AI | 1 天 |

总计约 **1-2 周**，流量捕获阶段最关键也最不确定。

## 4. 备选方案

### 4.1 公众号后台 API

**原理**：用户用**任意一个公众号**的管理权限扫码登录 `mp.weixin.qq.com` 后台，获取 session cookie + token。不需要是目标公众号的管理员——搜索 API 全局可用。

**API 链路**：
1. 扫码登录 → 获取 cookie + token
2. `cgi-bin/searchbiz` → 搜索目标公众号，获得 `fakeid`
3. `cgi-bin/appmsg?action=list_ex` → 拉取文章列表（标题、链接、封面、时间）
4. 直接访问 `/s?__biz=...` → 抓取文章正文（不需要凭证，但需反爬）

**参考项目**：

| 项目 | 技术栈 | 与 RSSGen 匹配度 |
|------|--------|-----------------|
| [wechat-download-api](https://github.com/tmwgsicp/wechat-download-api) | Python FastAPI + curl_cffi | **最高**（技术栈一致） |
| [WeRSS](https://github.com/rachelos/we-mp-rss) | Python FastAPI + Vue 3 | 高 |
| [wechat-article-exporter](https://github.com/wechat-article/wechat-article-exporter) | TypeScript Nuxt 3 | 低 |

**特点**：
- 优点：Token 有效期 ~4 天，无订阅数量限制，有完全开源的 Python 参考实现
- 缺点：需有任意一个公众号管理权限，每 4 天需重新扫码，文章正文抓取需代理池

### 4.2 微信读书 API

**原理**：扫码登录微信读书（weread.qq.com），通过其公众号订阅接口获取文章。

**特点**：
- 优点：无需公众号管理权限，扫码即用
- 缺点：每账号约 10 个订阅限制，Cookie 仅 2-3 天有效，**部分核心接口经过作者服务器转发（不完全开源）**，微信读书已开始对抗

### 4.3 微信本地数据库

**原理**：读取 PC 微信加密 SQLCipher 数据库，直接解析推送消息中的文章信息。

**特点**：
- 优点：不需要网络凭证，消息不漏；微信 V4 版本后数据库写入几乎"实时"
- 缺点：需解密数据库（每设备/账号密钥不同），只能获取订阅后的新推送，无法拉历史文章

## 5. 参考资源

### 5.1 开源项目

| 项目 | 原理 | 状态 |
|------|------|------|
| [pymmtls](https://github.com/ljc545w/pymmtls) | Python MMTLS 握手 | 活跃 |
| [Citizen Lab wechat-security-report](https://github.com/citizenlab/wechat-security-report) | MMTLS 逆向文档 + Frida 工具链 | 2024 年发布 |
| [WeWe RSS](https://github.com/cooderl/wewe-rss) | 微信读书 API | 部分不开源 |
| [wechat-article-exporter](https://github.com/wechat-article/wechat-article-exporter) | 后台 API + 代理池 | 活跃（8.8k stars） |
| [WeRSS](https://github.com/rachelos/we-mp-rss) | 后台 API (Python FastAPI) | 活跃 |
| [wechat-download-api](https://github.com/tmwgsicp/wechat-download-api) | 后台 API (Python FastAPI + curl_cffi) | 活跃 |

### 5.2 参考文档

- [微信文章导出工具原理](https://lijinma.com/how-wechat-article-export-tool-works/) — 代理池 + 96 节点架构
- [手机端 profile_ext 方案](https://www.sunnycandy.fun/post/python/get_wci_data/) — 参数分析
- [利用新接口抓取微信公众号的所有文章](https://cuiqingcai.com/4652.html) — 后台 API 的 `appmsg?action=list_ex` 翻页逻辑
- [微信公众号爬取研究](https://github.com/DropsDevopsOrg/ECommerceCrawlers/wiki/%E5%BE%AE%E4%BF%A1%E5%85%AC%E4%BC%97%E5%8F%B7%E7%88%AC%E5%8F%96%E7%A0%94%E7%A9%B6) — mitmproxy + Hook 方案
- [Should We Chat, Too?](https://citizenlab.ca/research/should-we-chat-too-security-analysis-of-wechats-mmtls-encryption-protocol/) — Citizen Lab MMTLS 安全分析论文
- [MMTLS at Black Hat Asia 2025](http://i.blackhat.com/Asia-25/Asia-25-Lin-Should-We-Chat-Too.pdf) — MMTLS 协议逆向技术细节

### 5.3 测试脚本

| 脚本 | 用途 |
|------|------|
| `test_wechat_profile.py` | 一次性验证 `action=home` + `action=getmsg`，从 HAR 读取凭证 |
| `test_key_lifetime.py` | key 有效期监控（每 10 分钟检测 + 失效熔断），部署在树莓派 24h 运行 |

## 6. 下一步

- [ ] 搭建 Android 模拟器环境（方案 A）或适配 Frida 到 PC 微信（方案 B）
- [ ] 捕获一次"打开公众号 profile"的 MMTLS 解密流量
- [ ] 分析 Protobuf 流量，定位 key 获取 API 并逆向 schema
- [ ] 基于 pymmtls 实现 Python MMTLS 客户端
- [ ] 集成到 RSSGen 微信路由
