/**
 * 生成 encrypt_corpus.json — 用于 sign_pure_py 的端到端回归测试。
 *
 * 输出每条样本结构：
 *   { input, b64, raw, r_calls: [{input, output}], x_calls: [{input, iv, output}] }
 *
 * raw = b64 经自定义字母表解码后的字节序列（即 JSVMP 内部最后一步 b64 编码前的实际字节）
 * r_calls = SM4 块加密 (__g.r) 的所有 I/O，r_calls[0] 是 IV 派生
 * x_calls = SM4-CBC (__g.x) 的所有 I/O
 *
 * 用法：node gen_corpus.js
 *
 * 注意：本脚本通过 hook __g.r / __g.x 抓取 I/O，并通过 b64 自定义字母表反解 raw。
 * 字母表从 sign_pure_py.py 同步。如果未来字母表变了，更新下面的 SHUFFLED_B64 即可。
 */
const fs = require('fs');

// 与 sign_pure_py.py 中的 _SHUFFLED_B64 保持一致
const SHUFFLED_B64 = '6fpLRqJO8M/c3jnYxFkUVC4ZIG12SiH=5v0mXDazWBTsuw7QetbKdoPyAl+hN9rgE';

function decodeShuffledB64(s) {
    const out = [];
    for (let i = 0; i < s.length; i += 4) {
        const c0 = SHUFFLED_B64.indexOf(s[i]);
        const c1 = SHUFFLED_B64.indexOf(s[i + 1]);
        const c2 = SHUFFLED_B64.indexOf(s[i + 2]);
        const c3 = SHUFFLED_B64.indexOf(s[i + 3]);
        out.push(((c0 & 0x3F) << 2) | (c1 >> 4));
        out.push(((c1 & 0xF) << 4) | (c2 >> 2));
        out.push(((c2 & 3) << 6) | c3);
    }
    return out;
}

// 暴露 __g 到 globalThis
let code = fs.readFileSync(__dirname + '/zhihu_sign.js', 'utf-8');
code = code.replace(
    'exports.XL = A,\n        exports.ZP = D',
    'globalThis.__g = __g; globalThis.__h = h; exports.XL = A, exports.ZP = D'
);
new Function(code)();

const g = globalThis.__g;

// hook __g.r 和 __g.x，每次 _encrypt 调用前清空、调用后归集
let r_calls = [];
let x_calls = [];

const orig_r = g.r;
g.r = function (input) {
    const inp = Array.from(input);
    const out = orig_r.call(this, input);
    r_calls.push({ input: inp, output: Array.from(out) });
    return out;
};

const orig_x = g.x;
g.x = function (input, iv) {
    const inp = Array.from(input);
    const ivArr = Array.from(iv);
    const out = orig_x.call(this, input, iv);
    x_calls.push({ input: inp, iv: ivArr, output: Array.from(out) });
    return out;
};

// 测试输入集：覆盖空 / 短 / 中 / 32 字节（签名链路实际场景）
const inputs = [
    '',
    'A', 'B', 'a', 'z',
    'AB', 'ABC', 'ABCD',
    'A'.repeat(12), 'A'.repeat(13), 'A'.repeat(14), 'A'.repeat(15), 'A'.repeat(16),
    // 35 个 32 字节随机十六进制字符串 (模拟 MD5 输出)
];
const hex = '0123456789abcdef';
for (let i = 0; i < 35; i++) {
    let s = '';
    for (let j = 0; j < 32; j++) s += hex[Math.floor(Math.random() * 16)];
    inputs.push(s);
}

const corpus = [];
for (const input of inputs) {
    r_calls = [];
    x_calls = [];
    const b64 = g._encrypt(input);
    corpus.push({
        input,
        b64,
        raw: decodeShuffledB64(b64),
        r_calls,
        x_calls,
    });
}

fs.writeFileSync(__dirname + '/encrypt_corpus.json', JSON.stringify(corpus, null, 2));
console.log(`Generated ${corpus.length} samples → encrypt_corpus.json`);
console.log(`  - 短输入 (< 14 B): ${corpus.filter(e => e.input.length < 14).length}`);
console.log(`  - 中等 (14-31 B): ${corpus.filter(e => e.input.length >= 14 && e.input.length < 32).length}`);
console.log(`  - 32 字节 (签名实际路径): ${corpus.filter(e => e.input.length === 32).length}`);
