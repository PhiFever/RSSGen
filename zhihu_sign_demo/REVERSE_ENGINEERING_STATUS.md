# 知乎签名逆向工程状态

## 目标

将 `zhihu_sign.js` 的签名逻辑完整迁移到纯 Python (`RSSGen/sign/zhihu/sign.py`)，使 `demo.py` 无需 `mini_racer` (V8 引擎) 即可生成相同签名。

## 整体进度：✅ 100% 完工 (2026-05-02)

> 🟢 重大突破 (2026-05-01): 通过外部资料找到了完整的后处理管线。
> 🟡 2026-05-02: 验证确认了 PKCS7 填充、SM4 正确性、B64 字母表。
> ✅ 2026-05-02: **全部完工**。在 `encrypt_corpus.json` 上 35/35 32 字节输入端到端 b64 byte-for-byte 匹配 JS 输出；
>   用真实登录 Cookie 调用知乎 API（问题接口 × 5 + moments + 复杂 query）全部 HTTP 200，返回真实数据。
>   `demo.py` 已切换到 `RSSGen/sign/zhihu/sign.py`，**完全移除 mini_racer / V8 依赖**。
>
> ⚠️ 之前文档中标记的「需要修改后的 ZK 轮密钥」是一个**误判**：`extract_constants.js` 抓到的 `h.zk` 已经是 JSVMP 实际加密所用值，
>   外部资料里那串「修改后 ZK」属于其它版本。当前 `ROUND_KEYS` 与 SM4 输出对 50 个随机块 + 35 个真实加密 corpus 全部匹配。

---

## ✅ 已完成

### 1. SM4 块加密 — 100%

- **文件**: `RSSGen/sign/zhihu/sign.py`
- 自定义 S-Box (256 字节)、SM4 块加密逻辑已验证正确
- `sm4_encrypt_block()` 与 JS `__g.r` 输出逐字节匹配（零块 + 50 个随机输入全部通过）
- **⚠️ 轮密钥问题**: 源码中的 `h.zk` 初始值会被 JSVMP 运行时原地修改。当前 `RSSGen/sign/zhihu/sign.py` 使用的是修改前的值，虽然 SM4 加密结果仍匹配 JS（因为 SM4 的正确性取决于 S-Box + RK 的一致性，而当前 S-Box 和 RK 是配对提取的），但**最终签名生成需要修改后的 ZK** 才能与服务器端一致。
  - 当前 ROUND_KEYS (修改前): `[1170614578, 1024848638, 1413669199, 3951632832, ...]`
  - 正确值 (修改后): `[1199388770, 946244156, 436498745, ...]` (来自外部资料)

### 2. IV 派生算法 — 100%

- **推导块结构** (16 字节):
  ```
  block[0]  = PERM(floor(rand * 127))  // 位变换，非原始随机字节
  block[1]  = 0x15 (固定常量)
  block[2:16] = data_area[0:14] XOR _IV_KEY
  ```
- **data_area 的 PKCS7 填充** (2026-05-02 确认):
  - 14 字节的数据区域先做 PKCS7 填充，再与 `_IV_KEY` XOR
  - 例如输入 "A" (1 字节): data_area = `[0x41] + [0x0D] × 13`
  - 例如空输入 "": data_area = `[0x0E] × 14`
  - 这解释了之前短输入的 IV block 不匹配问题，修复后全部 7 个 trace case 通过
- XOR 密钥 (14 字节): `0x13, 0x1a, 0x1f, 0x19, 0x4c, 0x1d, 0x4e, 0x1b, 0x1f, 0x4f, 0x1a, 0x1b, 0x4e, 0x1d`
- `IV = SM4_encrypt(block)`
- **rand_byte 位变换公式** (来自 cn-sec):
  ```python
  k = int(random() * 127)
  s = k & 0x1F
  result = (k & ~0x1F) + (((~s & 0x18) | (s & 0x04) | ((s ^ 2) & 0x03)) & 0x1F)
  ```

### 3. 输入分片与 PKCS7 填充 — 100%

- 输入的前 14 字节用于 IV 派生 (PKCS7 填充到 14 字节)
- 剩余字节 (input[14:]) 经 PKCS7 填充 (pad 值 = 14 = 0x0E) 后 SM4-CBC 加密
- 明文结构: `md5hex[14:31].charCode (18 字节) + [0x0E] × 14` = 32 字节
- MD5 输出总是 32 字节，所以实际签名场景下 overhead=0，不触发短输入边缘情况

### 4. SM4-CBC 加密 — 100%

- `cipher = SM4_CBC(plaintext, IV)` 产生 32 字节密文 (2 个块)
- `RSSGen/sign/zhihu/sign.py` 中的 `sm4_cbc_encrypt()` 实现已验证正确

### 5. 签名源字符串生成 — 100%

- `source = zse93 + "+" + url_path + "+" + d_c0 [+ "+" + body] [+ "+" + x_zst81]`
- MD5 哈希 → 32 字符 hex 字符串

### 6. 自定义 Base64 字母表 — 100% (2026-05-02 验证)

- 字母表: `6fpLRqJO8M/c3jnYxFkUVC4ZIG12SiH=5v0mXDazWBTsuw7QetbKdoPyAl+hN9rgE`
- 通过 `String.prototype.charAt` hook 从 JS 运行时直接捕获，与 `RSSGen/sign/zhihu/sign.py` 中一致
- 65 字符全部验证，`=` 作为数据字符（值 31），最后一个字符 `E`（值 64）
- 标准 4:3 base64 分组映射

---

## 🟡 后处理管线 — 算法已明确，待实现

### raw 格式的发现 (2026-05-02)

通过同时 hook `__g.r`/`__g.x` (捕获 SM4 输入/输出) 和 `String.prototype.charAt` (捕获 B64 编码的 6-bit 索引序列)，可以重建 JS 输出的精确 raw bytes。经过与 SM4 输出的对比分析：

- **raw bytes ≠ overhead + IV + CT** — 排除了简单拼接假设
- **raw bytes ≠ SM4(r[*].in/out)** — 排除了双重加密
- **raw bytes ≠ r[*].in XOR r[*].out** — 排除了 XOR 组合
- **raw bytes 与 SM4 输入/输出的 XOR 无固定模式** — 排除了常數 XOR

结论：JSVMP 在 B64 编码前对 SM4 输出做了**字节反转 + 位混洗 + XOR 常量**的复杂后处理，与外部资料描述的 Steps 7-10 一致。

### Step 7: 字节反转 + 拼接

```
X = reverse(cipher) ++ reverse(IV)
  = [cipher[31]..cipher[0], IV[15]..IV[0]]   // 48 字节
```

### Step 8: 位混洗 encode3 (16 组 × 3 字节)

对 X 每 3 字节 `(b0, b1, b2)` 执行：
```
out[3g+0] = ((b0 & 0x3F) << 2) | ((b1 >> 2) & 0x03)
out[3g+1] = ((b1 & 0x03) << 6) | ((b0 >> 6) << 4) | ((b2 & 0x03) << 2) | (b1 >> 6)
out[3g+2] = ((b1 & 0x30) << 2) | ((b2 >> 2) & 0x3F)
```

### Step 9: XOR 固定常量

```
CONST = [232, 0, 0, 2, 128, 192, 0, 8, 14, 0, 0, 0] × 4   // 48 字节
out48[i] ^= CONST[i]
```

### Step 10: 自定义 Base64 编码

- 字母表: `6fpLRqJO8M/c3jnYxFkUVC4ZIG12SiH=5v0mXDazWBTsuw7QetbKdoPyAl+hN9rgE`
- 标准 base64 分组 (每 3 字节 → 4 字符)
- 最终签名: `"2.0_" + base64(48字节)` → 68 字符

---

## ✅ 7. 后处理管线 (Steps 7-10) — 全部实装并验证 (2026-05-02)

实装位置：`RSSGen/sign/zhihu/sign.py` 中 `_encode3()` 函数 + `_POST_XOR_CONST` 常量 + `zhihu_encrypt()` 主流程。

```python
# 32 字节 MD5 hex 输入的实际签名管线
iv      = SM4(iv_block)                              # 16 字节
ct      = SM4_CBC(pkcs7(input[14:]), iv)             # 32 字节
X       = ct[::-1] + iv[::-1]                        # 48 字节 (Step 7)
shuf    = encode3(X)                                 # 16 组×3 字节位混洗 (Step 8)
raw     = shuf XOR ([0xE8,0,0,2,0x80,0xC0,0,8,0xE,0,0,0]×4)   # (Step 9)
sig     = "2.0_" + shuffled_b64_encode(raw)          # 64 字符 (Step 10)
```

验证：encrypt_corpus.json 中 35 个 32 字节输入逐字节匹配 JS 的 b64 输出。

## ✅ 8. random_byte 位变换 — 已实装

`_permute_random_byte(k)` 复现 `Math.random()*127` + 文档第 41-46 行的位变换。
对签名链路（32 字节 MD5）而言，random_byte 进了 SM4 加密不直接出现在最终输出，
但保留位变换以应对潜在的服务端高位校验。

## ✅ 9. 端到端真实请求验证 (2026-05-02)

| 测试 | 结果 |
|------|------|
| 同 question URL × 5 次 | 5/5 HTTP 200 |
| 多问题 ID | 全部 HTTP 200 (含 404 = 问题已删，非签名问题) |
| moments 动态接口 | HTTP 200 |
| 带复杂 query string 的 URL | HTTP 200 |
| corpus 比对 (35 × 32 字节输入) | 35/35 b64 字节级匹配 |

---

## 参考资源

| 来源 | 内容 |
|------|------|
| [cn-sec: Ai还原x-zse-96 vmp纯算](https://cn-sec.com/archives/5184884.html) | 完整算法管线 (Step 1-10)、rand_byte 公式、ZK 篡改发现 |
| [e-com-net: jsvmp-某乎 x-zes-96 算法还原](http://www.e-com-net.com/article/1739442513304895488.htm) | S[50] 结构、pop 循环 base64 编码细节 |
| [52pojie: 某乎x-zse-96签名算法python重写](https://www.52pojie.cn/thread-1631378-1-1.html) | 早期版本位运算算法 (参考) |

---

## 文件清单（2026-05-02 清理后）

详细分类与用法见 `REVERSE_ENGINEERING_GUIDE.md` §8。

| 文件 | 作用 |
|------|------|
| `RSSGen/sign/zhihu/sign.py` | **核心交付**。纯 Python 实现，无 V8 依赖 |
| `demo.py` | 端到端调用入口 |
| `zhihu_sign.js` | 知乎原始混淆 JS（约 3.7MB） |
| `extract_constants.js` | 提取 SBOX / ZK / D / 字母表 |
| `dump_sbox.js` | 备用：暴露 `__g` / `__h` 到全局后手工探索 |
| `decode_d.py` | 解码 D 数组中编码字符串 |
| `hook_sm4_round.js` | 生成 50 组 SM4 块向量 → `sm4_block_vectors.json` |
| `gen_corpus.js` | 生成完整加密 corpus → `encrypt_corpus.json` |
| `analyze_iv.js` | 系统性分析 IV 派生 |
| `encrypt_corpus.json` | 端到端验证基准（含 input/b64/raw/r_calls/x_calls） |
| `sm4_block_vectors.json` | 50 组 SM4 块加密 I/O |
| `REVERSE_ENGINEERING_GUIDE.md` | 全流程逆向教程 + 算法更新应对手册 |
| `README.md` | 项目说明 |

> 已删除 30+ 个分析过程产物（`capture_*.js` / `trace_*.js` / `hook_*.js` / `analyze_*.js`
> 和 5 份 zhihu_sign.js 备份）。如需查阅历史 hook 思路，可从 git 历史中翻找。

---

## 关键发现时间线

- 2026-04-27: 初始反混淆 + 常量提取
- 2026-04-29: SM4 加密实现 + 基础验证
- 2026-04-30: **突破** — Hook `__g.r`/`__g.x` 发现 IV 派生和 CBC 加密流程
- 2026-04-30: **突破** — D 数组完全解码 (143 条目)，发现打乱 base64 字母表
- 2026-04-30: **关键突破** — Proxy Hook 证实 S[53] = IV || CT，排除 VM 额外变换
- 2026-05-01: **发现** — Hook `g.r`/`g.x` 会污染 VM 状态
- 2026-05-01: **发现** — S[50] 结构 = header + input + PKCS7 padding
- 2026-05-01: **突破** — 通过外部资料获得完整算法管线：Step 7 (reverse) + Step 8 (encode3 位混洗) + Step 9 (XOR CONST) + Step 10 (自定义 base64)
- 2026-05-01: **发现** — ZK 轮密钥被 JSVMP 运行时篡改，需重新提取
- 2026-05-02: **验证** — PKCS7 填充在 IV 派生 data_area 中的作用，修复后全部 7 个 trace case 通过
- 2026-05-02: **验证** — SM4 块加密与 JS 完全一致（零块 + 50 随机向量）
- 2026-05-02: **验证** — 自定义 base64 字母表通过 charAt hook 确认为正确
- 2026-05-02: **确认** — raw bytes 格式非简单 overhead+IV+CT，与外部管线的后处理步骤一致
- 2026-05-02: **生成** — `encrypt_corpus.json` (48 组)、`sm4_block_vectors.json` (50 组) 供后续验证
