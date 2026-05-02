"""
知乎签名 Python 纯实现 - 不依赖 mini_racer / Node.js

基于对 zhihu_sign.js 的逆向分析：
1. 组合 source 字符串（zse93 + url_path + d_c0 + body + x_zst_81）
2. MD5(source) → 32 字符 hex 字符串
3. zhihu_encrypt(md5_hex):
   a. 前 14 字节用于 IV 派生 (r[0].in = random || 0x15 || (input[0:14] XOR KEY))
   b. 后 18 字节 PKCS7 填充后 CBC 加密 (IV = SM4(r[0].in))
   c. 输出: 开销字节 + IV + 密文 → 打乱 base64 编码
4. 最终签名: "2.0_" + signature
"""

import hashlib
import os
import struct
import re
import argparse
import sys
import urllib.parse
from pathlib import Path
from typing import Optional

# ============================================
# 从 zhihu_sign.js 运行时提取的 SM4 常量
# ============================================

# S-Box (256 bytes) - 从 window.__ZH__.zse.zb 提取
SBOX = [
    20, 223, 245, 7, 248, 2, 194, 209, 87, 6, 227, 253, 240, 128, 222, 91,
    237, 9, 125, 157, 230, 93, 252, 205, 90, 79, 144, 199, 159, 197, 186, 167,
    39, 37, 156, 198, 38, 42, 43, 168, 217, 153, 15, 103, 80, 189, 71, 191,
    97, 84, 247, 95, 36, 69, 14, 35, 12, 171, 28, 114, 178, 148, 86, 182,
    32, 83, 158, 109, 22, 255, 94, 238, 151, 85, 77, 124, 254, 18, 4, 26,
    123, 176, 232, 193, 131, 172, 143, 142, 150, 30, 10, 146, 162, 62, 224, 218,
    196, 229, 1, 192, 213, 27, 110, 56, 231, 180, 138, 107, 242, 187, 54, 120,
    19, 44, 117, 228, 215, 203, 53, 239, 251, 127, 81, 11, 133, 96, 204, 132,
    41, 115, 73, 55, 249, 147, 102, 48, 122, 145, 106, 118, 74, 190, 29, 16,
    174, 5, 177, 129, 63, 113, 99, 31, 161, 76, 246, 34, 211, 13, 60, 68,
    207, 160, 65, 111, 82, 165, 67, 169, 225, 57, 112, 244, 155, 51, 236, 200,
    233, 58, 61, 47, 100, 137, 185, 64, 17, 70, 234, 163, 219, 108, 170, 166,
    59, 149, 52, 105, 24, 212, 78, 173, 45, 0, 116, 226, 119, 136, 206, 135,
    175, 195, 25, 92, 121, 208, 126, 139, 3, 75, 141, 21, 130, 98, 241, 40,
    154, 66, 184, 49, 181, 46, 243, 88, 101, 183, 8, 23, 72, 188, 104, 179,
    210, 134, 250, 201, 164, 89, 216, 202, 220, 50, 221, 152, 140, 33, 235, 214,
]

# 轮密钥 (32 个 uint32) - 从 window.__ZH__.zse.zk 提取
ROUND_KEYS_SIGNED = [
    1170614578, 1024848638, 1413669199, -343334464, -766094290, -1373058082, -143119608, -297228157,
    1933479194, -971186181, -406453910, 460404854, -547427574, -1891326262, -1679095901, 2119585428,
    -2029270069, 2035090028, -1521520070, -5587175, -77751101, -2094365853, -1243052806, 1579901135,
    1321810770, 456816404, -1391643889, -229302305, 330002838, -788960546, 363569021, -1947871109,
]

ROUND_KEYS = [k & 0xFFFFFFFF for k in ROUND_KEYS_SIGNED]

# ============================================
# 打乱 Base64 字母表 (从 VM D 数组提取, D[136..140])
# ============================================
_SHUFFLED_B64 = '6fpLRqJO8M/c3jnYxFkUVC4ZIG12SiH=5v0mXDazWBTsuw7QetbKdoPyAl+hN9rgE'
assert len(_SHUFFLED_B64) == 65
assert len(set(_SHUFFLED_B64)) == 65

# 14 字节 IV 派生 XOR 密钥 (从 r[0].in XOR input[0:14] 提取)
_IV_KEY = bytes([0x13, 0x1a, 0x1f, 0x19, 0x4c, 0x1d, 0x4e, 0x1b, 0x1f, 0x4f, 0x1a, 0x1b, 0x4e, 0x1d])

# 后处理 XOR 常量 (12 字节 × 4 = 48 字节)
_POST_XOR_CONST = bytes([232, 0, 0, 2, 128, 192, 0, 8, 14, 0, 0, 0]) * 4


# ============================================
# SM4 加密核心 (同标准 SM4 结构, 自定义 S-Box / 轮密钥)
# ============================================

def _bytes_to_uint32(b: bytes, offset: int = 0) -> int:
    return (b[offset] << 24) | (b[offset + 1] << 16) | (b[offset + 2] << 8) | b[offset + 3]


def _uint32_to_bytes(v: int) -> bytes:
    return bytes([(v >> 24) & 0xFF, (v >> 16) & 0xFF, (v >> 8) & 0xFF, v & 0xFF])


def _rotl(v: int, n: int) -> int:
    return ((v << n) | (v >> (32 - n))) & 0xFFFFFFFF


def _sm4_l_transform(v: int) -> int:
    return v ^ _rotl(v, 2) ^ _rotl(v, 10) ^ _rotl(v, 18) ^ _rotl(v, 24)


def _sm4_t_transform(v: int) -> int:
    b0 = SBOX[(v >> 24) & 0xFF]
    b1 = SBOX[(v >> 16) & 0xFF]
    b2 = SBOX[(v >> 8) & 0xFF]
    b3 = SBOX[v & 0xFF]
    w = (b0 << 24) | (b1 << 16) | (b2 << 8) | b3
    return _sm4_l_transform(w)


def sm4_encrypt_block(plaintext: bytes) -> bytes:
    """SM4 加密单个 16 字节块"""
    assert len(plaintext) == 16

    X = [0] * 36
    X[0] = _bytes_to_uint32(plaintext, 0)
    X[1] = _bytes_to_uint32(plaintext, 4)
    X[2] = _bytes_to_uint32(plaintext, 8)
    X[3] = _bytes_to_uint32(plaintext, 12)

    for i in range(32):
        x_new = X[i] ^ _sm4_t_transform(X[i + 1] ^ X[i + 2] ^ X[i + 3] ^ ROUND_KEYS[i])
        X[i + 4] = x_new

    result = bytearray(16)
    result[0:4] = _uint32_to_bytes(X[35])
    result[4:8] = _uint32_to_bytes(X[34])
    result[8:12] = _uint32_to_bytes(X[33])
    result[12:16] = _uint32_to_bytes(X[32])
    return bytes(result)


def sm4_cbc_encrypt(plaintext: bytes, iv: bytes) -> bytes:
    """SM4 CBC 加密 (同 JS __g.x 函数)"""
    assert len(iv) == 16
    assert len(plaintext) % 16 == 0

    result = bytearray()
    prev = bytearray(iv)

    for i in range(0, len(plaintext), 16):
        block = plaintext[i:i + 16]
        xored = bytes(b ^ prev[j] for j, b in enumerate(block))
        encrypted = sm4_encrypt_block(xored)
        result.extend(encrypted)
        prev = bytearray(encrypted)

    return bytes(result)


# ============================================
# 打乱 Base64 编解码
# ============================================

def shuffled_b64_encode(data: bytes) -> str:
    """使用打乱字母表的 base64 编码"""
    result = []
    for i in range(0, len(data), 3):
        b1 = data[i]
        b2 = data[i + 1] if i + 1 < len(data) else 0
        b3 = data[i + 2] if i + 2 < len(data) else 0

        c1 = b1 >> 2
        c2 = ((b1 & 3) << 4) | (b2 >> 4)
        c3 = ((b2 & 15) << 2) | (b3 >> 6)
        c4 = b3 & 63

        result.append(_SHUFFLED_B64[c1])
        result.append(_SHUFFLED_B64[c2])
        result.append(_SHUFFLED_B64[c3])
        result.append(_SHUFFLED_B64[c4])
    return ''.join(result)


def shuffled_b64_decode(b64_str: str) -> bytes:
    """使用打乱字母表的 base64 解码"""
    result = []
    for i in range(0, len(b64_str), 4):
        c0 = _SHUFFLED_B64.index(b64_str[i])
        c1 = _SHUFFLED_B64.index(b64_str[i + 1])
        c2 = _SHUFFLED_B64.index(b64_str[i + 2])
        c3 = _SHUFFLED_B64.index(b64_str[i + 3])
        b1 = ((c0 & 0xFF) << 2) | (c1 >> 4)
        b2 = ((c1 & 15) << 4) | (c2 >> 2)
        b3 = ((c2 & 3) << 6) | c3
        result.extend([b1 & 255, b2 & 255, b3 & 255])
    return bytes(result)


# ============================================
# 加密函数 (完全匹配 JS __g._encrypt)
# ============================================

def _pkcs7_pad(data: bytes, block_size: int = 16) -> bytes:
    """PKCS7 填充"""
    pad_len = block_size - (len(data) % block_size)
    return data + bytes([pad_len] * pad_len)


def _permute_random_byte(k: int) -> int:
    """对 Math.random() * 127 产生的字节做位变换 (来自 JSVMP 还原)

    保留高位 (bit 5-6)，低 5 位按固定置换表打乱。
    服务端可能验证此模式，因此实现保留以保安全边界。
    """
    k &= 0x7F  # 限制在 [0, 127]
    s = k & 0x1F
    return (k & ~0x1F) | (((~s & 0x18) | (s & 0x04) | ((s ^ 2) & 0x03)) & 0x1F)


def _encode3(x: bytes) -> bytes:
    """48 字节位混洗 (16 组 × 3 字节)

    每 3 字节 (b0, b1, b2) 重新打散到 3 个输出字节，混合各字节的不同 bit 位。
    """
    assert len(x) % 3 == 0, f"_encode3 需要 3 字节对齐，收到 {len(x)}"
    out = bytearray(len(x))
    for g in range(len(x) // 3):
        b0, b1, b2 = x[3 * g], x[3 * g + 1], x[3 * g + 2]
        out[3 * g + 0] = (((b0 & 0x3F) << 2) | ((b1 >> 2) & 0x03)) & 0xFF
        out[3 * g + 1] = (((b1 & 0x03) << 6) | ((b0 >> 6) << 4) | ((b2 & 0x03) << 2) | (b1 >> 6)) & 0xFF
        out[3 * g + 2] = (((b1 & 0x30) << 2) | ((b2 >> 2) & 0x3F)) & 0xFF
    return bytes(out)


def _build_iv_derivation_block(input_bytes: bytes, random_byte: int) -> bytes:
    """构建 IV 派生块 (对应 r[0].in)

    14 字节数据区域先 PKCS7 填充，再与 _IV_KEY XOR。
    """
    n = min(len(input_bytes), 14)
    data_area = bytearray(14)
    for i in range(n):
        data_area[i] = input_bytes[i]

    pad_byte = 14 - n
    if pad_byte > 0:
        for i in range(n, 14):
            data_area[i] = pad_byte

    block = bytearray(16)
    block[0] = random_byte & 0xFF
    block[1] = 0x15
    for i in range(14):
        block[2 + i] = data_area[i] ^ _IV_KEY[i]

    return bytes(block)


def zhihu_encrypt(data: str) -> str:
    """知乎加密函数 - 完全匹配 JS __g._encrypt(encodeURIComponent(er))

    对于 32 字节 MD5 hex 输入 (签名链路唯一会走的路径):
      1. input[0:14] PKCS7 填充 → XOR _IV_KEY → 拼 [random_byte, 0x15] → SM4 → IV
      2. input[14:32] PKCS7 填充到 32 字节 → SM4-CBC(IV) → CT (32 字节)
      3. X = reverse(CT) ++ reverse(IV)              # 48 字节
      4. shuffled = _encode3(X)                       # 16 组 × 3 字节位混洗
      5. raw = shuffled XOR _POST_XOR_CONST
      6. return shuffled_b64_encode(raw)              # 64 字符
    """
    input_bytes = data.encode('ascii')

    # JSVMP 内部使用 Math.random() * 127 后做位变换；为对齐潜在的服务端校验保留同样的范围
    k = int.from_bytes(os.urandom(1), 'big') % 127
    random_byte = _permute_random_byte(k)

    iv_block = _build_iv_derivation_block(input_bytes[:14], random_byte)
    iv = sm4_encrypt_block(iv_block)

    tail = input_bytes[14:]
    if len(tail) > 0:
        ct = sm4_cbc_encrypt(_pkcs7_pad(tail, 16), iv)
    else:
        ct = b''

    # 后处理管线：reverse → encode3 → XOR CONST → shuffled b64
    X = ct[::-1] + iv[::-1]
    if len(X) % 3 != 0:
        # 短输入 (< 14 字节) 走的是另一条管线，签名链路不会触发
        raise NotImplementedError(f"短输入后处理未实现 (X len = {len(X)})")

    shuffled = _encode3(X)
    raw = bytes(a ^ b for a, b in zip(shuffled, _POST_XOR_CONST[:len(shuffled)]))
    return shuffled_b64_encode(raw)


# ============================================
# URL 解析 (对应 JS 中的 e8 函数)
# ============================================

def parse_url_path(url: str) -> str:
    """提取 URL 路径部分 (同 JS 中 e8 函数)"""
    if not url.startswith('http'):
        url = "https://www.zhihu.com" + (url if url.startswith('/') else '/' + url)

    idx = url.find('/', url.find('://') + 3)
    if idx >= 0:
        return url[idx:]
    return '/'


# ============================================
# 签名生成主函数
# ============================================

def gen_source(url: str, body: Optional[str], zse93: str, dc0: str, x_zst81: Optional[str]) -> str:
    """生成签名源字符串"""
    path = parse_url_path(url)

    body_str = None
    if body is not None:
        body_str = str(body)

    parts = [zse93, path, dc0]

    if body_str and len(body_str) <= 4096:
        parts.append(body_str)

    if x_zst81:
        parts.append(x_zst81)

    return '+'.join(parts)


# 签名版本常量
X_ZSE_93_VERSION = "101_3_3.0"
X_ZSE_96_PREFIX = "2.0_"


def get_signature(url: str, d_c0: str, body: str = "") -> dict:
    """生成知乎签名 (纯 Python 实现)"""
    zse93 = X_ZSE_93_VERSION

    source = gen_source(
        url=url,
        body=body if body else None,
        zse93=zse93,
        dc0=d_c0,
        x_zst81=None,
    )

    md5_hex = hashlib.md5(source.encode('ascii')).hexdigest()
    signature = zhihu_encrypt(md5_hex)

    return {
        "source": source,
        "x_zse_93": zse93,
        "x_zse_96": X_ZSE_96_PREFIX + signature
    }


def _encrypt_with_fixed_random(data: str, random_byte: int) -> str:
    """同 zhihu_encrypt 但接受外部 random_byte，仅供回归测试用。"""
    input_bytes = data.encode('ascii')
    iv_block = _build_iv_derivation_block(input_bytes[:14], random_byte)
    iv = sm4_encrypt_block(iv_block)
    tail = input_bytes[14:]
    ct = sm4_cbc_encrypt(_pkcs7_pad(tail, 16), iv) if tail else b''
    X = ct[::-1] + iv[::-1]
    if len(X) % 3 != 0:
        return None
    shuffled = _encode3(X)
    raw = bytes(a ^ b for a, b in zip(shuffled, _POST_XOR_CONST[:len(shuffled)]))
    return shuffled_b64_encode(raw)


def main():
    """回归测试: 用 encrypt_corpus.json 中的真实 JS 输出验证全管线"""
    import json

    assert len(set(SBOX)) == 256, "SBOX 不是双射"

    # corpus 由 zhihu_sign_demo/gen_corpus.js 生成，与本模块跨目录共用
    repo_root = Path(__file__).resolve().parents[3]
    corpus_path = repo_root / 'zhihu_sign_demo' / 'encrypt_corpus.json'
    if not corpus_path.exists():
        print(f"⚠️  {corpus_path} 不存在，跳过对比验证")
        return

    with open(corpus_path) as f:
        corpus = json.load(f)

    samples_32 = [e for e in corpus if len(e['input']) == 32]
    print(f"对 {len(samples_32)} 个 32 字节输入做端到端 B64 比对（签名链路实际路径）")

    pass_count = 0
    for e in samples_32:
        rand_byte = e['r_calls'][0]['input'][0]
        py_b64 = _encrypt_with_fixed_random(e['input'], rand_byte)
        if py_b64 == e['b64']:
            pass_count += 1
        else:
            print(f"  FAIL  input={e['input'][:16]}...")
            print(f"    py: {py_b64}")
            print(f"    js: {e['b64']}")

    print(f"\n✅ 端到端: {pass_count}/{len(samples_32)} 通过")
    assert pass_count == len(samples_32), "回归失败"

    # 演示一次签名生成
    print("\n签名生成示例:")
    demo = get_signature(
        "https://www.zhihu.com/api/v4/questions/659012275/answers?limit=5",
        "ABC123def456=|1700000000",
    )
    print(f"  source  : {demo['source']}")
    print(f"  x-zse-93: {demo['x_zse_93']}")
    print(f"  x-zse-96: {demo['x_zse_96']}  (len={len(demo['x_zse_96'])})")
    assert demo['x_zse_96'].startswith('2.0_')
    assert len(demo['x_zse_96']) == 68


if __name__ == '__main__':
    main()
