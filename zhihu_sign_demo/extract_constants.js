/**
 * 提取 zhihu_sign.js 中的 SM4 常量（S-Box 和轮密钥）
 * 用法: node extract_constants.js
 */
const fs = require('fs');
const path = require('path');

// 加载 zhihu_sign.js
const js_code = fs.readFileSync(path.join(__dirname, 'zhihu_sign.js'), 'utf-8');

// 执行 JS 代码
eval(js_code);

// 从 window.__ZH__.zse 提取 S-Box 和轮密钥
const h = globalThis.window.__ZH__.zse;

console.log("// S-Box (h.zb) - 256 bytes:");
console.log("SBOX =", JSON.stringify(Array.from(h.zb)));

console.log("\n// Round Keys (h.zk) - 32 uint32 values:");
console.log("ROUND_KEYS =", JSON.stringify(Array.from(h.zk)));

// 测试 __g._encrypt 是否等价于 __g.x
// 提取 __g
let __g_val;
// 需要从模块作用域中获取 __g - 通过测试加密函数来验证

// 测试签名
const test_url = "https://www.zhihu.com/api/v4/questions/659012275/answers?limit=5";
const test_dc0 = "6ZfUnN1NrRuPTsr4auj8yRrtiPbyCX6wUPg=|1768266713";

// 复制 tv 函数中的逻辑
const e8 = function(er) {
    if (!er.startsWith('http')) {
        er = "https://www.zhihu.com" + (er.startsWith('/') ? er : '/' + er);
    }
    var idx = er.indexOf('/', er.indexOf('://') + 3);
    return idx >= 0 ? er.substring(idx) : '/';
};

const result = globalThis.tv(test_url, "", {zse93: "101_3_3.0", dc0: test_dc0, xZst81: null}, "");

console.log("\n// 测试签名结果:");
console.log("source:", result.source);
console.log("signature:", result.signature);

// 也输出 MD5(source)
const crypto = require('crypto');
const md5_source = crypto.createHash('md5').update(result.source).digest('hex');
console.log("MD5(source):", md5_source);
console.log("MD5(source) length:", md5_source.length);
