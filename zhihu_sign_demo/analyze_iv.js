/**
 * Characterize the IV derivation function f(input[0:14]) → r0.in[1:16]
 */
const fs = require('fs');
let code = fs.readFileSync(__dirname + '/zhihu_sign.js', 'utf-8');

code = code.replace(
    'exports.XL = A,\n        exports.ZP = D',
    'globalThis.__g = __g;\n        globalThis.__h = h;\n        exports.XL = A,\n        exports.ZP = D'
);

const fn = new Function(code);
fn();

const g = globalThis.__g;
const h = globalThis.__h;
const sbox = Array.from(h.zb).map(b => b & 255);

// Hook __g.r ONLY
const orig_r = g.r;
let r0_in;
g.r = function(er) {
    if (!r0_in) {
        r0_in = Array.from(er).map(b => b & 255);
    }
    return orig_r.call(this, er);
};

console.log("=== IV Derivation Function Mapping ===");
console.log("Format: input → r0.in[1:16]");
console.log("");

function getR0In(input) {
    r0_in = null;
    g._encrypt(input);
    return r0_in ? r0_in.slice(1) : null;
}

// Test: systematic variation
const results = [];

// Baseline: all zeros
const allZero = "00000000000000";
const allZeroR0 = getR0In(allZero);
console.log(`all-0:     "${allZero}"`);
console.log(`r0.in[1:]: ${allZeroR0.map(b => b.toString(16).padStart(2,'0')).join(' ')}`);

// All same hex chars
for (const ch of ['0', '1', 'a', 'f']) {
    const input = ch.repeat(14);
    const r0 = getR0In(input);
    console.log(`all-${ch}:    "${input}"`);
    console.log(`r0.in[1:]: ${r0.map(b => b.toString(16).padStart(2,'0')).join(' ')}`);

    // XOR with all-zero baseline
    if (allZeroR0) {
        const xor = r0.map((b, i) => b ^ allZeroR0[i]);
        console.log(`XOR w/ 0: ${xor.map(b => b.toString(16).padStart(2,'0')).join(' ')}`);
    }
    results.push({ label: `all-${ch}`, input, r0 });
}

// Single byte variation
console.log("\n--- Single byte variation (changing byte at position) ---");
for (let pos = 0; pos < 14; pos++) {
    let chars = allZero.split('');
    chars[pos] = 'f';
    const input = chars.join('');
    const r0 = getR0In(input);
    const diff = r0.map((b, i) => b ^ allZeroR0[i]);
    const changedBytes = diff.map((b, i) => b !== 0 ? `${i}:${b.toString(16)}` : null).filter(x => x);
    console.log(`pos${pos}=f: r0=${r0.map(b => b.toString(16).padStart(2,'0')).join(' ')}`);
    console.log(`  changed: [${changedBytes.join(', ')}]`);
}

// Check if r0.in[1:] = input_bytes XOR some_key
// The key might be in the data array
console.log("\n--- Checking XOR with input ---");
for (const {label, input, r0} of results) {
    const inputBytes = Array.from(input).map(c => c.charCodeAt(0));
    const xor16 = inputBytes.map((b, i) => b ^ r0[i]);
    // Also add a 0 for the 15th byte
    const xor15 = [...inputBytes, 0].map((b, i) => b ^ r0[i]);
    console.log(`${label}: r0 XOR input[:14] = ${xor16.map(b => b.toString(16).padStart(2,'0')).join(' ')}`);
}

// Try S-box transform
console.log("\n--- Checking S-box relationships ---");
for (const {label, input, r0} of results) {
    const inputBytes = Array.from(input).map(c => c.charCodeAt(0));
    const sboxed = inputBytes.map(b => sbox[b]);
    const xor_sbox = sboxed.map((b, i) => b ^ r0[i]);
    console.log(`${label}: S[input] XOR r0 = ${xor_sbox.map(b => b.toString(16).padStart(2,'0')).join(' ')}`);
}

// The problem might involve the data array D
// D entries look like obfuscated strings: "kU..." or "jT..."
// Maybe they need to be decoded first
console.log("\n--- Data array inspection ---");
console.log(`h has ${Object.keys(h).length} keys: ${Object.keys(h).join(', ')}`);
console.log(`zm: ${Array.from(h.zm).map(b => b.toString(16).padStart(2,'0')).join(' ')}`);
