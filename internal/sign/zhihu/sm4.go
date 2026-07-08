package zhihu

// SM4 加密核心实现（自定义 S-Box 和轮密钥，与知乎 JS 中的实现一致）

// sbox 是从 zhihu_sign.js 运行时提取的 S-Box（256 字节）。
var sbox = [256]byte{
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
}

// roundKeys 是从 zhihu_sign.js 提取的轮密钥（32 个 uint32）。
// 以有符号 int32 声明，init() 中转为 uint32（对应 Python 的 ROUND_KEYS_SIGNED → ROUND_KEYS）。
var roundKeys [32]uint32

func init() {
	signed := [32]int32{
		1170614578, 1024848638, 1413669199, -343334464,
		-766094290, -1373058082, -143119608, -297228157,
		1933479194, -971186181, -406453910, 460404854,
		-547427574, -1891326262, -1679095901, 2119585428,
		-2029270069, 2035090028, -1521520070, -5587175,
		-77751101, -2094365853, -1243052806, 1579901135,
		1321810770, 456816404, -1391643889, -229302305,
		330002838, -788960546, 363569021, -1947871109,
	}
	for i, k := range signed {
		roundKeys[i] = uint32(k)
	}
}

// bytesToUint32 将 4 字节大端序转为 uint32。
func bytesToUint32(b []byte, offset int) uint32 {
	return uint32(b[offset])<<24 | uint32(b[offset+1])<<16 | uint32(b[offset+2])<<8 | uint32(b[offset+3])
}

// uint32ToBytes4 将 uint32 转为 4 字节大端序。
func uint32ToBytes4(v uint32) [4]byte {
	return [4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
}

// rotl32 32 位循环左移。
func rotl32(v uint32, n uint) uint32 {
	return (v << n) | (v >> (32 - n))
}

// sm4LTransform SM4 L 线性变换。
func sm4LTransform(v uint32) uint32 {
	return v ^ rotl32(v, 2) ^ rotl32(v, 10) ^ rotl32(v, 18) ^ rotl32(v, 24)
}

// sm4TTransform SM4 T 变换（S-Box 替换 + L 变换）。
func sm4TTransform(v uint32) uint32 {
	b0 := uint32(sbox[(v>>24)&0xFF])
	b1 := uint32(sbox[(v>>16)&0xFF])
	b2 := uint32(sbox[(v>>8)&0xFF])
	b3 := uint32(sbox[v&0xFF])
	w := b0<<24 | b1<<16 | b2<<8 | b3
	return sm4LTransform(w)
}

// sm4EncryptBlock SM4 加密单个 16 字节块。
func sm4EncryptBlock(plaintext []byte) []byte {
	if len(plaintext) != 16 {
		panic("sm4EncryptBlock: 输入必须为 16 字节")
	}

	var X [36]uint32
	X[0] = bytesToUint32(plaintext, 0)
	X[1] = bytesToUint32(plaintext, 4)
	X[2] = bytesToUint32(plaintext, 8)
	X[3] = bytesToUint32(plaintext, 12)

	for i := 0; i < 32; i++ {
		X[i+4] = X[i] ^ sm4TTransform(X[i+1]^X[i+2]^X[i+3]^roundKeys[i])
	}

	result := make([]byte, 16)
	b35 := uint32ToBytes4(X[35])
	b34 := uint32ToBytes4(X[34])
	b33 := uint32ToBytes4(X[33])
	b32 := uint32ToBytes4(X[32])
	copy(result[0:4], b35[:])
	copy(result[4:8], b34[:])
	copy(result[8:12], b33[:])
	copy(result[12:16], b32[:])
	return result
}

// sm4CBCEncrypt SM4 CBC 模式加密。plaintext 必须是 16 字节的倍数。
func sm4CBCEncrypt(plaintext, iv []byte) []byte {
	if len(iv) != 16 {
		panic("sm4CBCEncrypt: IV 必须为 16 字节")
	}
	if len(plaintext)%16 != 0 {
		panic("sm4CBCEncrypt: plaintext 必须是 16 字节的倍数")
	}

	result := make([]byte, 0, len(plaintext))
	prev := make([]byte, 16)
	copy(prev, iv)

	for i := 0; i < len(plaintext); i += 16 {
		block := plaintext[i : i+16]
		xored := make([]byte, 16)
		for j := 0; j < 16; j++ {
			xored[j] = block[j] ^ prev[j]
		}
		encrypted := sm4EncryptBlock(xored)
		result = append(result, encrypted...)
		copy(prev, encrypted)
	}

	return result
}
