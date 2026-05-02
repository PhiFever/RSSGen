const fs = require('fs');
let code = fs.readFileSync(__dirname + '/zhihu_sign.js', 'utf-8');

// Patch to expose __g and its internal state
code = code.replace(
    'exports.XL = A,\n        exports.ZP = D',
    `globalThis.__g = __g;
        globalThis.__h = h;
        // Dump internal arrays of __g
        const desc = Object.getOwnPropertyDescriptors(__g);
        const keys = Object.keys(__g);
        globalThis.__g_keys = keys;
        globalThis.__g_values = {};
        for (const k of keys) {
            try { globalThis.__g_values[k] = __g[k]; } catch(e) {}
        }
        exports.XL = A,
        exports.ZP = D`
);

const fn = new Function(code);
fn();

const g = globalThis.__g;
console.log('__g keys:', JSON.stringify(globalThis.__g_keys));

// Dump all array-like values
for (const k of globalThis.__g_keys) {
    const v = g[k];
    if (v && typeof v === 'object' && typeof v.length === 'number' && v.length > 0 && v.length <= 300) {
        const arr = Array.from(v).map(x => typeof x === 'number' ? x : String(x));
        console.log(`\n__g.${k} (len=${v.length}):`);
        console.log(JSON.stringify(arr));
    }
}
