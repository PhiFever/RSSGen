# 知乎 x-zse-96 签名逆向全流程分析

> 写给未来的自己：如果将来知乎更新了签名算法，这份文档帮你快速重建心智模型，
> 而不是从「先去看一遍混淆 JS」开始。

---

## 0. TL;DR — 算法成品

**输入**：URL、d_c0 (cookie)、可选 body、可选 x-zst-81

**输出**：`x-zse-96 = "2.0_" + 64 字符自定义 b64`

**主链路（10 步管线）**：

```
Step 1.  source = "101_3_3.0" + "+" + url_path + "+" + d_c0 [+ "+" + body] [+ "+" + x_zst81]
Step 2.  md5_hex = MD5(source)                        # 32 字符 ASCII hex
Step 3.  input = md5_hex.encode('ascii')              # 32 字节
Step 4.  iv_block[0]    = permute(rand & 0x7F)        # 7 位随机数 + 位变换
         iv_block[1]    = 0x15
         iv_block[2..]  = pkcs7(input[0:14], 14) XOR _IV_KEY   # 14 字节
Step 5.  iv = SM4_encrypt_block(iv_block)             # 自定义 SBOX + ZK 的 SM4
Step 6.  ct = SM4_CBC(pkcs7(input[14:32], 16), iv)    # 32 字节
Step 7.  X = ct[::-1] ++ iv[::-1]                     # 48 字节 (字节级反转后拼接)
Step 8.  shuf = encode3(X)                            # 16 组×3 字节位混洗
Step 9.  raw = shuf XOR ([0xE8,0,0,2,0x80,0xC0,0,8,0xE,0,0,0] × 4)
Step 10. sig = "2.0_" + custom_b64(raw)               # 自定义字母表
```

**密码学元件**（全部从 zhihu_sign.js 运行时提取，详见 §3.2）：
- 自定义 SBOX (256 字节)
- 32 个 SM4 轮密钥 ZK (uint32)
- 14 字节 IV XOR 密钥 `_IV_KEY`
- 12 字节后处理 XOR 常量 `_POST_XOR_CONST`
- 65 字符自定义 base64 字母表

---

## 1. 反爬类型识别（决定后续工具选择）

第一步：用 `curl` 不带签名直接请求知乎 API：

```bash
curl https://www.zhihu.com/api/v4/questions/659012275/answers
```

返回 `{"error": {"code": 40362, "message": "ZSE Code Error", ...}}` —— 服务端要求 `x-zse-96`。

**识别结论**：行为型反爬。HTTP 200 正常加载页面，但业务接口要求自定义签名头。**不是**瑞数 / Akamai 那种 412 挑战型。

**对应路径**：路径 A（算法追踪）。最终交付纯协议 Python，不需要补 jsdom/sdenv 环境。

---

## 2. 抓包定位签名生成代码

### 2.1 发起调用栈追踪

在浏览器 DevTools 中：
1. 找一个业务 API 请求（如 `/api/v4/questions/.../answers`），看 Request Headers 里的 `x-zse-93` 和 `x-zse-96`
2. 在 Network 面板右键 → "Initiator" 看调用栈
3. 顺着栈往下，第一个出现 `_encrypt` / `signature` / `2.0_` 的栈帧就是签名函数

### 2.2 zhihu_sign.js 的暴露面

JS 入口函数（demo.py 旧版调用的 `tv` 函数）：

```js
tv(url, body, { zse93: "101_3_3.0", dc0, xZst81 }, cookie)
  → 返回 { source, signature }
```

内部调用链（混淆后）：
```
tv → 拼 source → MD5(source) → __g._encrypt(md5_hex) → 自定义 b64 → signature
```

`__g._encrypt` 就是核心黑盒。__g 内部的 `r` 和 `x` 是 SM4 块加密和 CBC 加密。

---

## 3. JSVMP 识别与不反编译策略

### 3.1 为什么是 JSVMP

打开 zhihu_sign.js（约 200KB）会看到：

```js
function l() { /* VM 实例 */ }
l.prototype.O = function(bytecode, ...) {
  while (true) {
    var op = bytecode[pc++];
    switch (op) { case 1: ...; case 2: ...; /* 几十个 case */ }
  }
}
// __g._encrypt 内部：
var eo = new l;
eo.S = t;            // SBOX 数组
eo.S[0] = input;     // 输入塞进 S[0]
eo.O(h.G, h.V, h.D); // 跑字节码
return eo.C[3];      // 输出从 C[3] 取
```

特征：
- 自定义解释器 + 字节码（`h.G` 是 2345 条字节码）
- 用大数组（`h.D`，143 条数据）存编码后的字符串/常量
- `S` 数组同时是 SBOX 和工作寄存器

### 3.2 关键决定：不反编译字节码

字节码反编译收益极低、成本极高。**正确策略：把 VM 当黑盒，hook 它对外可见的 I/O**。

可观测的 I/O 点：
| Hook 点 | 看到什么 | 价值 |
|---------|---------|------|
| `__g.r(in)` | SM4 单块加密 I/O | 能看到 IV 派生过程 |
| `__g.x(in)` | SM4-CBC 加密 I/O + IV | 能看到主密文 |
| `String.prototype.charAt` | b64 编码用到的字母索引 | 能反推最终 raw 字节序 |
| `eo.S[*]` 写入（Proxy 拦截） | VM 内部状态 | 能确认 IV‖CT 在 S 数组里的存放位置 |

### 3.3 提取静态常量（一次性）

`extract_constants.js` / `dump_sbox.js` 做的事：

```js
// 在 zhihu_sign.js 编译执行时把 __g 和 h 暴露到 globalThis
code = code.replace('exports.XL = A,\n        exports.ZP = D',
                    'globalThis.__g = __g; globalThis.__h = h; ...');
new Function(code)();

// 然后直接读对象属性
const SBOX      = Array.from(__h.zb);   // 256 字节
const ROUND_KEYS = Array.from(__h.zk);  // 32 个 uint32
const D_ARRAY    = __h.D;               // 143 条编码字符串（含 b64 字母表）
```

⚠️ **关键认知**：`extract_constants.js` 抓到的 `h.zk` 是 JSVMP **已完成初始化后**的值。
后续 50 个随机块测试 + 35 个真实 corpus 端到端验证都证明这个值就是加密实际所用值。
`REVERSE_ENGINEERING_STATUS.md` 早期版本里说的「ZK 被运行时修改、需要重新提取」是误判，
源自外部文章（[cn-sec](https://cn-sec.com/archives/5184884.html)）描述的另一版本。

### 3.4 自定义 b64 字母表的提取

字母表藏在 D 数组里某条编码字符串中。两种提取方式：
1. **静态解码 D 数组**（`decode_d.py`）：D 数组里每条字符串经 XOR 解码后能拿到。
2. **动态 hook**（`capture_combined.js`）：`String.prototype.charAt` 在 b64 编码时被高频调用，
   收集所有调用产生的字符即字母表。

两种方式得到同一字母表：
```
6fpLRqJO8M/c3jnYxFkUVC4ZIG12SiH=5v0mXDazWBTsuw7QetbKdoPyAl+hN9rgE
```
65 字符（含 `=` 作为数据字符，值 31）。

---

## 4. 算法各层的逆向过程

### 4.1 SM4 块加密 (`__g.r`)

**Hook 设置**（不污染 VM）：

```js
const orig_r = __g.r;
__g.r = function(input) {
  const inp = Array.from(input);
  const out = orig_r.call(this, input);
  console.log({ in: inp, out: Array.from(out) });
  return out;
};
```

⚠️ 注意：早期版本用 Proxy 包装会影响 VM 的 `this` 绑定，导致输出错乱。直接函数替换更安全。

**还原方法**：知乎用的是国密 SM4 标准结构（32 轮 Feistel-like + L 变换），但 SBOX 和轮密钥都是自定义的。
用 `SM4(SBOX, ZK)` 代入零块、随机块对比输出（`hook_sm4_round.js` 生成 50 组测试向量），逐字节匹配通过即可确认。

### 4.2 IV 派生（最容易踩坑的一层）

观察 `r[0].in` (16 字节)：
- byte 0：每次都不一样 → **随机**
- byte 1：永远是 `0x15` → **常量**
- byte 2..15：与 input 有关 → **数据区**

数据区 `r[0].in[2:16]` 与 `input[0:14]` 的关系：
```python
# 各取一组 trace 做 XOR 看模式
for byte i in range(14):
    diff[i] = r[0].in[2+i] XOR input[i]
# 多组样本对齐 → 发现 diff 在不同输入下保持一致
# diff = [0x13, 0x1a, 0x1f, 0x19, 0x4c, 0x1d, 0x4e, 0x1b, 0x1f, 0x4f, 0x1a, 0x1b, 0x4e, 0x1d]
# → 这就是 _IV_KEY
```

**坑点**：当 `input` 短于 14 字节时（比如空字符串、单字符），上面的 XOR 公式不成立。
真相：input 先做 **PKCS7 填充到 14 字节**，再 XOR `_IV_KEY`。
```python
# 空输入: data_area = [0x0E] * 14   (pad 值 = 14)
# "A":    data_area = [0x41] + [0x0D] * 13  (pad 值 = 13)
# 14 字节及以上：data_area = input[0:14]，无填充
```

### 4.3 SM4-CBC（`__g.x`）

直接 hook 看 I/O，对比标准 CBC：
- IV 来自 `__g.r(iv_block)` 的输出
- plaintext 是 `pkcs7(input[14:], 16)`
- mode = CBC（前一块密文 XOR 后一块明文 → SM4 块加密）

签名场景下 input = 32 字节 hex（MD5 输出），所以 `input[14:32]` 是 18 字节，PKCS7 填充到 32 字节，输出 32 字节 CT。

### 4.4 后处理管线（最难破的一层）

#### 4.4.1 排除简单拼接假设

最朴素的猜测：`raw = overhead + iv + ct`，然后 b64。
实测：JS 实际输出的 raw（通过 `String.charAt` hook 反向解码 b64）跟 `iv+ct` 完全对不上。

#### 4.4.2 排除其他容易猜的假设

```
raw == overhead + iv + ct          ❌
raw == SM4(r[*].in or out)         ❌  (排除二次加密)
raw == r[*].in XOR r[*].out         ❌
raw == something XOR fixed_const    ⚠️  (XOR 后看到弱模式但不完全)
```

第 4 项 "XOR fixed_const" 给了线索：尝试 `raw[0] ^ 0xE8 == 0` 在多个 trace 中成立，
说明输出确实经过了固定常量 XOR，但 XOR 之前的 byte 顺序与 IV/CT 不同。

#### 4.4.3 借助外部资料补完

[cn-sec 的 AI 还原文档](https://cn-sec.com/archives/5184884.html) 描述了同类知乎签名的后处理：
- Step 7：`reverse(cipher) ++ reverse(IV)`（字节级反转）
- Step 8：`encode3` 位混洗（对每 3 字节 (b0,b1,b2) 做特定的 bit 重排，输出 3 字节）
- Step 9：XOR 12 字节常量 × 4
- Step 10：自定义 base64

**端到端验证**（30 行 Python 即可）：
```python
X = ct[::-1] + iv[::-1]
shuf = encode3(X)
raw = bytes(a ^ b for a, b in zip(shuf, CONST))
# 与 corpus["raw"] 比较 → 35/35 完全匹配 ✅
```

#### 4.4.4 encode3 位混洗公式

```python
# 每 3 字节 (b0, b1, b2) → 输出 3 字节
out[0] = ((b0 & 0x3F) << 2) | ((b1 >> 2) & 0x03)
out[1] = ((b1 & 0x03) << 6) | ((b0 >> 6) << 4) | ((b2 & 0x03) << 2) | (b1 >> 6)
out[2] = ((b1 & 0x30) << 2) | ((b2 >> 2) & 0x3F)
```

设计意图猜测：这个变换是**双射**（可逆），不增加密码学强度，纯粹是混淆 + 让结果"看起来像随机"。
和自定义 b64 字母表配合，让别人简单观察输出无法识别 SM4 结构。

### 4.5 random_byte 位变换

`iv_block[0]` 是个伪随机字节。它来自：
```js
k = Math.floor(Math.random() * 127);   // 0..126
s = k & 0x1F;
result = (k & ~0x1F) + (((~s & 0x18) | (s & 0x04) | ((s ^ 2) & 0x03)) & 0x1F);
```

特征：
- 高 2 位（bit 5,6）保留
- 低 5 位经一个固定置换表打乱（bijection on [0, 31]）
- 输出 ∈ [0, 127]，bit 7 永远 0

**对签名安全影响**：在 32 字节 MD5 路径下，这个字节经 SM4 加密后才出现在最终输出，
理论上服务端无法验证它的"真假"。但保留位变换实现，以防服务端做 bit 7 校验或类似检查。

---

## 5. 验证方法论（测试金字塔）

### Layer 1：模块级（每改一处算法跑一次）

`SBOX 双射` + `SM4 单块对零块` → 30 秒确认基础对齐。

### Layer 2：corpus 比对（最有用的一层）

`encrypt_corpus.json` 是 48 组 `{ input, b64, raw, r_calls, x_calls }` 真实 JS 输出。
关键技巧：**用 `r_calls[0].input[0]` 作为固定 random_byte 输入 Python 实现**，
这样消除随机性，纯 Python 输出可以与 JS b64 byte-for-byte 比对。

```python
def test_with_fixed_random(input_str, js_random_byte):
    iv_block = build_iv_block(input_str[:14], js_random_byte)
    iv = sm4_block(iv_block)
    ct = sm4_cbc(pkcs7(input_str[14:], 16), iv)
    X = ct[::-1] + iv[::-1]
    raw = encode3(X) XOR CONST
    return custom_b64(raw)
# 对 35 个 32 字节样本 → 35/35 通过
```

35/35 byte-for-byte 通过 = 算法 100% 等价。

### Layer 3：端到端真实请求

最终验证 = 用真实 d_c0 cookie 发请求，看服务器是否 200 返回。
踩雷点：服务端可能"静默拒绝"（200 + 空 body），需要校验 `data` 字段非空。

```python
# 多 URL × 多次连续请求，确认稳定性（≥ 5 次）
# 因为每次随机字节不同，服务端如果拒绝某些随机字节，会出现间歇性失败
```

---

## 6. 错误路径回顾（避免重复踩坑）

### 6.1 「ZK 被运行时修改」误判

外部文章描述 JSVMP 会原地改写轮密钥。基于此推断 → 试图写脚本捕获修改后的 ZK → 反复挫败。
**反思**：这个外部文章描述的是知乎的**另一个版本**。在你这版 zhihu_sign.js 上，
`extract_constants.js` 抓到的 `h.zk` 已经是 JSVMP 实际加密用的值。
**判定方法**：用当前 ZK 跑 SM4，与 `__g.r` hook 输出对比，匹配 = 不需要重提取。

### 6.2 hook 污染 VM 状态

最早用 `Proxy` 包装 `__g.r` / `__g.x`：
```js
__g.r = new Proxy(orig_r, { apply(t, this_, args) {...} });  // ❌ 改变 this 绑定
```
**正确做法**：直接函数替换 + 显式 call：
```js
const orig_r = __g.r;
__g.r = function(input) { const out = orig_r.call(this, input); ...; return out; };
```

### 6.3 短输入的边界情况

短输入（空、"A"、"AB"、…< 14 字节）走的不是同一条 raw 拼接路径。
但**签名链路输入永远是 32 字节 MD5 hex**，短输入分支永远不会触发。
所以：**不要为短输入特别还原**。`zhihu_encrypt` 在 `len(X) % 3 != 0` 时直接 raise 即可。

### 6.4 漏看已有数据 vs 找新方向

后处理管线卡了将近 1 天。其实 `encrypt_corpus.json` 里同时抓了 `iv`、`ct`、`raw` 三组数据，
配合外部资料的公式（reverse + encode3 + XOR）做交叉验证只需要 30 行 Python。
**反思**：被外部文章里的"看起来很复杂的描述"先入为主，没意识到「我已经有了所有需要的数据」。
**教训**：遇到僵局先盘点已有的 ground truth，再决定继续抓还是先验证。

---

## 7. 算法更新时的应对策略

如果某天发现签名失效（HTTP 40362），按下面的顺序排查：

### 7.1 先做差异定位（10 分钟级）

不要一上来就重新逆向。先确认**哪一层变了**：

| 假设 | 验证方法 |
|------|---------|
| zse93 版本号变 | 抓最新页面的请求头，看 `x-zse-93` 是否还是 `101_3_3.0` |
| URL 路径规则变 | 看 demo.py 输出的 source 字符串和真实抓包对不对 |
| 签名长度变 | 真实抓包的 `x-zse-96` 长度还是 68 吗？长度变 = 算法骨架变 |
| 签名前缀变 | 还是 `2.0_` 吗？变了 = 整套换了 |

### 7.2 抓最新 zhihu_sign.js + diff

```bash
# 在浏览器 Network 面板找 sm4 / sign 关键字的 JS 文件
# diff 旧版 vs 新版，看变动量
```

### 7.3 重新提取常量（按变动量决定深度）

```bash
node extract_constants.js   # 自动重新跑，输出 SBOX/ZK/D
node dump_sbox.js           # 备用：暴露 __g + __h 后手工读
```

把新常量替换到 `RSSGen/sign/zhihu/sign.py` 的 `SBOX` / `ROUND_KEYS_SIGNED` / `_SHUFFLED_B64` / `_IV_KEY` / `_POST_XOR_CONST` 五个位置。

### 7.4 跑 corpus 测试 → 二分定位变动层

如果常量更新后 corpus 还不通过，哪一层变了？逐层验证：

```python
# 先验 SM4：__g.r(iv_block) 与 Python 输出比
# 再验 IV 派生：iv_block 构造规则变了？
# 再验 CBC：CBC 模式是否还是标准 CBC？
# 再验后处理：reverse / encode3 / CONST / 字母表
```

每一层都有独立的 trace 数据可以隔离测试。

### 7.5 重新生成 corpus

```bash
node gen_corpus.js          # 跑 48 个真实加密样本 → encrypt_corpus.json
node hook_sm4_round.js      # 跑 50 个 SM4 块测试向量 → sm4_block_vectors.json
```

corpus 是测试金字塔 Layer 2 的基础，必须与最新 JS 同步。

---

## 8. 文件清单（已清理，仅保留核心 + 重逆向所需）

### 8.1 生产代码（位于仓库主目录，**不在本 demo 目录下**）
- `RSSGen/sign/zhihu/sign.py` — **核心交付**。纯 Python 签名实现，无 V8 依赖。
- `RSSGen/sign/zhihu/zhihu_sign.js` — 知乎原始混淆签名 JS（约 3.7MB）。
- `RSSGen/routes/zhihu.py` — 业务路由，调用 `sign.py` 生成 `x-zse-96` 头。

### 8.2 demo 目录文档（学习/重逆向用）
- `demo.py` — 端到端调用入口（cookie + URL → API 响应），导入 `RSSGen.sign.zhihu.sign`。
- `README.md` — 项目说明与运行方式。
- `REVERSE_ENGINEERING_STATUS.md` — 完工状态与时间线。
- `REVERSE_ENGINEERING_GUIDE.md` — 本文档。

### 8.3 常量提取（重新逆向时必用）
- `extract_constants.js` — 提取 SBOX / ZK / D 数组 / 字母表（首选）
- `dump_sbox.js` — 备用：暴露 `__g` / `__h` 到 globalThis 后手工探索
- `decode_d.py` — 解码 D 数组中编码字符串（含 b64 字母表）

### 8.4 动态追踪与数据生成
- `hook_sm4_round.js` — Hook `__g.r` 生成 50 组 SM4 块向量 → `sm4_block_vectors.json`
- `gen_corpus.js` — 生成完整加密 corpus → `encrypt_corpus.json`
- `analyze_iv.js` — 系统性分析 IV 派生（多输入下的 `r[0].in` 对比）

### 8.5 数据文件（验证基准）
- `encrypt_corpus.json` — **最重要**。`{input, b64, raw, r_calls, x_calls}` 端到端样本
- `sm4_block_vectors.json` — 50 组 SM4 块加密 I/O，纯算法验证用

### 已删除文件备忘
原仓库有大量分析过程产物（`capture_*.js` / `trace_*.js` / `hook_*.js` / `analyze_*.js` 等
20+ 文件 + 5 份 zhihu_sign.js 备份）。这些都是探索过程的中间脚本，
当时帮助理解了 VM 内部结构（如 S 数组结构、b64 编码循环位置等），
**但完工后没有持续价值**——上面 §8.3 + §8.4 的工具足以覆盖未来重新逆向场景。
如果遇到新的疑难场景需要参考历史 hook 模式，从 git 历史里翻就行。

---

## 9. 一些反思（写给自己）

### 9.1 工具不能替代假设管理

DeepSeek 卡住的根源不是工具不行，是**假设没及时验证**。
"ZK 需要重提取" 这个假设挂了将近一天，期间没有用「拿当前 ZK 跑一遍 SM4 对比 hook 输出」这个 5 分钟测试去否定它。
**教训**：每个会影响后续方向的假设，都要写出来 + 设计一个最便宜的证伪实验。

### 9.2 已有数据的边际价值最高

corpus 抓完之后，**所有后续问题都应该先问"corpus 里能不能验证"**。
我用 30 行 Python 验完 Steps 7-9 的全部假设。如果先验证再决定下一步，就不会走到「再去搞 hook」那条岔路。

### 9.3 外部资料是参考不是真理

cn-sec 的文章描述的是同类算法的**某个版本**。
公式可以借鉴（encode3 那个位混洗确实正确），但「ZK 被运行时修改」这个具体断言对当前版本不成立。
**教训**：从外部资料拿来的具体数值/断言，都要在自己的环境中重新验证。
公式 / 思路可以复用，具体值不能假设跨版本一致。

### 9.4 简单实现优先于完整实现

`RSSGen/sign/zhihu/sign.py` 最初版本试图同时支持「短输入 + 长输入」两条管线。
实际上签名链路 100% 是 32 字节 MD5 路径，短输入分支永远不会被签名调用触发。
为不会被调用的代码花精力 = 浪费。
**教训**：实装时先确认输入空间，对不会发生的情况直接抛异常即可。

---

## 附录 A：encode3 位混洗的 bit 级图示

输入 3 字节 (b0, b1, b2)，每字节 8 bit：

```
b0:  [b0_7 b0_6 b0_5 b0_4 b0_3 b0_2 b0_1 b0_0]
b1:  [b1_7 b1_6 b1_5 b1_4 b1_3 b1_2 b1_1 b1_0]
b2:  [b2_7 b2_6 b2_5 b2_4 b2_3 b2_2 b2_1 b2_0]
```

输出：
```
out[0] = b0[5..0] | b1[7..6]              # b0 低 6 + b1 高 2
       = [b0_5 b0_4 b0_3 b0_2 b0_1 b0_0 b1_7 b1_6]
out[1] = b1[1..0] | b0[7..6] | b2[1..0] | b1[7..6]
       = [b1_1 b1_0 b0_7 b0_6 b2_1 b2_0 b1_7 b1_6]   ⚠️ 注意 b1[7..6] 被复用
out[2] = b1[5..4] | b2[7..2]
       = [b1_5 b1_4 b2_7 b2_6 b2_5 b2_4 b2_3 b2_2]
```

⚠️ `out[1]` 中 `b1[7..6]` 被读取了两次（一次到 out[1] 的 bit 7-6，一次到 bit 1-0）。
这是公式 `((b1 & 0x03) << 6) | ((b0 >> 6) << 4) | ((b2 & 0x03) << 2) | (b1 >> 6)` 里最后那个 `(b1 >> 6)` 项的来源。
也就是说 encode3 **不是双射**——`b1` 的高 2 位被重复编码、低 2 位也被编码，所以 24 bit 输入只产生 22 bit 独立信息。
解码时如果要还原 `b1[7..6]` 必须依赖一致性约束（两处读出来的应一致）。

实际上签名只需要"编码 → b64 → 服务端验证"，不需要逆向 encode3，所以这个非双射性不影响。

---

## 附录 B：源字符串拼接规则

观察多个 zhihu_sign.js 调用样本，源字符串组装逻辑：

```python
parts = ["101_3_3.0", url_path, d_c0]

if body and len(body) <= 4096:
    parts.append(body)

if x_zst81:           # 仅在某些接口（如发布答案）需要
    parts.append(x_zst81)

source = "+".join(parts)
```

注意：
- url_path 是去除 host 后的部分，**包含 query string**（`?limit=5&...`）
- url_path **不做 URL 解码**——和服务端发出的 URL 完全一致就行
- body 在 ≤ 4096 字节时参与，超过则只用 URL；这个阈值可能随版本变
- d_c0 包含 `=|` 和时间戳后缀，整段原样拼接

---

*文档版本：2026-05-02*
*对应 zhihu_sign.js 实际生效版本：见 `extract_constants.js` 输出时间戳*
