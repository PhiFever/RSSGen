#!/usr/bin/env node
/**
 * 知乎签名生成 - 无 jsdom 版
 */

// 加载轻量版初始化（不依赖 jsdom）
require('./zhihu_lite.js');

const args = process.argv.slice(2);
const url = args[0] || '';
const d_c0 = args[1] || '';

if (!url || !d_c0) {
    console.error('用法: node sign_minimal.js <url> <d_c0>');
    process.exit(1);
}

try {
    // tv 函数已在 zhihu_lite.js 中导出到全局
    const result = tv(url, "", {
        zse93: "101_3_3.0",
        dc0: d_c0,
        xZst81: null
    }, "");

    console.log(JSON.stringify({
        source: result.source,
        x_zse_93: "101_3_3.0",
        x_zse_96: "2.0_" + result.signature
    }, null, 2));
} catch (e) {
    console.error('签名失败:', e.message);
    console.error(e.stack);
    process.exit(1);
}