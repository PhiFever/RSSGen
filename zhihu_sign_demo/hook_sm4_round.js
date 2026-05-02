/**
 * 深度 hook SM4 block encrypt (__g.r) 来获取每一轮的输入输出
 * 通过 hook VM 的 XOR 操作来追踪
 */
const fs = require('fs');
let code = fs.readFileSync(__dirname + '/zhihu_sign.js', 'utf-8');

// Patch to expose __g
code = code.replace(
    'exports.XL = A,\n        exports.ZP = D',
    'globalThis.__g = __g; globalThis.__h = h; exports.XL = A, exports.ZP = D'
);

const fn = new Function(code);
fn();

const g = globalThis.__g;

// Test: compute r([0]*16) and compare with Python
// First, let's extract the actual algorithm by hooking SM4 with a wrapper

// Create a buffer to capture intermediate operations  
const orig_r = g.r;
let round_trace = [];

// We need to trace at a lower level. Let's wrap the r function
// and trace the input/output for each call.
// The r function is: function(er) { ... } where er is a Uint8Array(16)

// Let's test our Python SM4 against JS SM4 with the same input
const test_input = new Uint8Array(16);  // all zeros
const js_output = orig_r.call(g, test_input);
console.log('JS r([0]*16):', Array.from(js_output).map(b => b.toString(16).padStart(2,'0')).join(''));

// Test with the trace input for "A"
const trace_input = new Uint8Array([0x6c,0x15,0x52,0x17,0x12,0x14,0x41,0x10,0x43,0x16,0x12,0x42,0x17,0x16,0x43,0x10]);
const trace_output = orig_r.call(g, trace_input);
console.log('JS r(trace_in):', Array.from(trace_output).map(b => b.toString(16).padStart(2,'0')).join(''));

// Test encrypt for a real signature
const test_md5 = 'a62595d0a9b590c792536ba283e3763a';
const enc_result = g._encrypt(test_md5);
console.log('JS _encrypt(MD5):', enc_result);

// Generate comprehensive test vectors for SM4 block cipher
const test_vectors = [];
for (let i = 0; i < 50; i++) {
    const input = new Uint8Array(16);
    for (let j = 0; j < 16; j++) input[j] = Math.floor(Math.random() * 256);
    const output = orig_r.call(g, input);
    test_vectors.push({
        input: Array.from(input),
        output: Array.from(output)
    });
}
fs.writeFileSync(__dirname + '/sm4_block_vectors.json', JSON.stringify(test_vectors, null, 2));
console.log('Generated 50 SM4 block test vectors');

// Also save SBOX and RK for verification
const h = globalThis.__h;
console.log('\n=== Verify SBOX and RK ===');
// Dump from the VM's actual memory

// The __g.r function accesses SBOX via `this` context
// Let's see what 'this' refers to in __g.r
// Modify r to capture this
const orig_r_bound = g.r;
const captured_this = {};
const proxy_r = new Proxy(orig_r_bound, {
    apply(target, thisArg, args) {
        if (!captured_this.done) {
            captured_this.done = true;
            captured_this.constructor = thisArg.constructor.name;
            captured_this.keys = Object.keys(thisArg);
            // Dump all numeric arrays
            for (const key of Object.keys(thisArg)) {
                const val = thisArg[key];
                if (val && typeof val === 'object' && typeof val.length === 'number' && val.length > 0 && val.length <= 300) {
                    captured_this[key] = { len: val.length, data: Array.from(val).slice(0, 20) };
                }
            }
        }
        return Reflect.apply(target, thisArg, args);
    }
});

// Hmm, this approach is complex. Let me just use the existing trace data
// and focus on fixing the Python SM4.

// The key question: is the JS SM4 standard SM4?
// Let's check by computing standard SM4 on a known test vector

// Standard SM4 test vector (from GMT 0002-2012):
// Key: 0123456789abcdeffedcba9876543210
// Plaintext: 0123456789abcdeffedcba9876543210
// Ciphertext: 681edf34d206965e86b3e94f536e4246

// Our __g.r uses a pre-expanded key schedule (32 round keys)
// and a custom SBOX. Let me try to verify the SBOX was extracted correctly.

// Let me check: does the SBOX in __g's context match what we extracted?
// The SBOX is used through the VM's this.S array
// The __g.r function accesses this.S[some_index] for SBOX lookups

// Actually, let me try to get the SBOX from the VM context
// The SBOX is initialized during (new l).O("ABt7CAAU...") in the VM
// After init, the SBOX is stored somewhere accessible to __g.r

// Let me hook the VM to capture the SBOX directly
console.log('\nAttempting to capture SBOX from VM context...');

// The VM stores SBOX in this.S which is an array
// __g.r is called with `this` being the VM instance (l)
// So this.S contains the SBOX

// We can create a VM instance and call r on it
// But __g.r is a bound method... let me check

// Actually from the decompiled _encrypt:
// function(er) {
//     var eo = new l;
//     eo.S = t;       // 't' is the SBOX array
//     eo.S[0] = er;    // Set first element to input? No, 'er' is the input argument
//     eo.O(h.G, h.V, h.D);  // Run bytecode
//     return eo.C[3];  // Return result from C register
// }

// Wait! 'eo.S = t' sets the S array. And then 'eo.S[0] = er' sets S[0] to the input!
// So the input is passed through S[0], and the output is in C[3]!

// This means the encrypt function:
// 1. Creates a new VM instance 'eo'
// 2. Sets eo.S = t (t is the SBOX + state array)
// 3. Sets eo.S[0] = input (the plaintext to encrypt)
// 4. Runs the bytecode starting at pc=V
// 5. Returns eo.C[3] (the result)

// So the SBOX is in 't' which is a captured variable in the encrypt closure!

// The S array in the VM serves dual purpose: SBOX lookups AND state registers
// The first elements of S (before SBOX) might be working registers

// Let me capture 't' by creating a hook
