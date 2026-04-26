#!/usr/bin/env node
/**
 * 知乎 x-zse-96 签名生成脚本 (包装 zhihu.js)
 *
 * 使用方法: node sign_gen.js <url> <d_c0>
 */

// 先加载 zhihu.js (它会初始化所有加密模块)
require('./zhihu.js');

// 从命令行获取参数
const url = process.argv[2];
const d_c0 = process.argv[3];

if (!url || !d_c0) {
    console.error('用法: node sign_gen.js <url> <d_c0>');
    console.error('示例: node sign_gen.js "https://www.zhihu.com/api/v4/questions/123/answers" "AXCWcRzPxxaPT..."');
    process.exit(1);
}

// zhihu.js 加载后会定义全局 tv 函数和 e2, eC 等辅助函数
// tv 函数签名: tv(url, body, {zse93, dc0, xZst81}, encryptor)

try {
    const result = tv(url, "", {
        zse93: "101_3_3.0",
        dc0: d_c0,
        xZst81: null
    }, "");

    const signature = "2.0_" + result.signature;

    // 输出 JSON 格式结果
    console.log(JSON.stringify({
        url: url,
        d_c0: d_c0,
        source: result.source,
        x_zse_93: "101_3_3.0",
        x_zse_96: signature
    }, null, 2));
} catch (e) {
    console.error('签名生成失败:', e.message);
    console.error(e.stack);
    process.exit(1);
}