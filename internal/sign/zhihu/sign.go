package zhihu

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"math/rand"
	"strings"
)

// ============================================
// 常量
// ============================================

// shuffledB64 是从 JS VM D 数组提取的打乱 base64 字母表。
const shuffledB64 = "6fpLRqJO8M/c3jnYxFkUVC4ZIG12SiH=5v0mXDazWBTsuw7QetbKdoPyAl+hN9rgE"

// ivKey 是 14 字节 IV 派生 XOR 密钥。
var ivKey = []byte{0x13, 0x1A, 0x1F, 0x19, 0x4C, 0x1D, 0x4E, 0x1B, 0x1F, 0x4F, 0x1A, 0x1B, 0x4E, 0x1D}

// postXORConst 是后处理 XOR 常量（12 字节重复 4 次 = 48 字节）。
var postXORConst = func() []byte {
	c := []byte{232, 0, 0, 2, 128, 192, 0, 8, 14, 0, 0, 0}
	result := make([]byte, 48)
	for i := 0; i < 4; i++ {
		copy(result[i*12:(i+1)*12], c)
	}
	return result
}()

// 版本常量
const (
	xzse93Version = "101_3_3.0"
	xzse96Prefix  = "2.0_"
)

// ============================================
// 打乱 Base64 编解码
// ============================================

// buildB64Lookup 构建字符→索引的查找表。
func buildB64Lookup() [128]int {
	var table [128]int
	for i := range table {
		table[i] = -1
	}
	for i, c := range shuffledB64 {
		table[c] = i
	}
	return table
}

var b64Lookup = buildB64Lookup()

// shuffledB64Encode 使用打乱字母表的 base64 编码。
func shuffledB64Encode(data []byte) string {
	var result strings.Builder
	result.Grow((len(data) + 2) / 3 * 4)

	for i := 0; i < len(data); i += 3 {
		b1 := data[i]
		var b2, b3 byte
		if i+1 < len(data) {
			b2 = data[i+1]
		}
		if i+2 < len(data) {
			b3 = data[i+2]
		}

		c1 := b1 >> 2
		c2 := ((b1 & 3) << 4) | (b2 >> 4)
		c3 := ((b2 & 15) << 2) | (b3 >> 6)
		c4 := b3 & 63

		result.WriteByte(shuffledB64[c1])
		result.WriteByte(shuffledB64[c2])
		result.WriteByte(shuffledB64[c3])
		result.WriteByte(shuffledB64[c4])
	}
	return result.String()
}

// ============================================
// 位变换函数
// ============================================

// permuteRandomByte 对随机字节做位变换（来自 JSVMP 还原）。
// 保留高位 (bit 5-6)，低 5 位按固定置换表打乱。
func permuteRandomByte(k int) byte {
	k &= 0x7F
	s := k & 0x1F
	return byte((k &^ 0x1F) | (((^s & 0x18) | (s & 0x04) | ((s ^ 2) & 0x03)) & 0x1F))
}

// encode3 对 48 字节做位混洗（16 组 x 3 字节）。
func encode3(x []byte) []byte {
	if len(x)%3 != 0 {
		panic(fmt.Sprintf("encode3: 需要 3 字节对齐，收到 %d", len(x)))
	}
	out := make([]byte, len(x))
	for g := 0; g < len(x)/3; g++ {
		b0 := x[3*g]
		b1 := x[3*g+1]
		b2 := x[3*g+2]
		out[3*g+0] = byte(((b0 & 0x3F) << 2) | ((b1 >> 2) & 0x03))
		out[3*g+1] = byte(((b1 & 0x03) << 6) | ((b0 >> 6) << 4) | ((b2 & 0x03) << 2) | (b1 >> 6))
		out[3*g+2] = byte(((b1 & 0x30) << 2) | ((b2 >> 2) & 0x3F))
	}
	return out
}

// ============================================
// 加密函数
// ============================================

// pkcs7Pad PKCS7 填充。
func pkcs7Pad(data []byte, blockSize int) []byte {
	padLen := blockSize - (len(data) % blockSize)
	padded := make([]byte, len(data)+padLen)
	copy(padded, data)
	for i := len(data); i < len(padded); i++ {
		padded[i] = byte(padLen)
	}
	return padded
}

// buildIVDerivationBlock 构建 IV 派生块（对应 r[0].in）。
func buildIVDerivationBlock(inputBytes []byte, randomByte byte) []byte {
	n := len(inputBytes)
	if n > 14 {
		n = 14
	}

	dataArea := make([]byte, 14)
	copy(dataArea, inputBytes[:n])

	padByte := 14 - n
	if padByte > 0 {
		for i := n; i < 14; i++ {
			dataArea[i] = byte(padByte)
		}
	}

	block := make([]byte, 16)
	block[0] = randomByte
	block[1] = 0x15
	for i := 0; i < 14; i++ {
		block[2+i] = dataArea[i] ^ ivKey[i]
	}

	return block
}

// reverseBytes 返回字节切片的反转副本。
func reverseBytes(b []byte) []byte {
	n := len(b)
	r := make([]byte, n)
	for i := 0; i < n; i++ {
		r[i] = b[n-1-i]
	}
	return r
}

// zhihuEncrypt 知乎加密函数 - 匹配 JS __g._encrypt。
func zhihuEncrypt(data string) (string, error) {
	inputBytes := []byte(data)

	// Math.random() * 127 后做位变换
	k := int(rand.Int31n(127))
	randomByte := permuteRandomByte(k)

	ivBlock := buildIVDerivationBlock(inputBytes[:min(len(inputBytes), 14)], randomByte)
	iv := sm4EncryptBlock(ivBlock)

	tail := inputBytes[14:]
	var ct []byte
	if len(tail) > 0 {
		ct = sm4CBCEncrypt(pkcs7Pad(tail, 16), iv)
	}

	X := make([]byte, len(ct)+len(iv))
	copy(X, reverseBytes(ct))
	copy(X[len(ct):], reverseBytes(iv))

	if len(X)%3 != 0 {
		return "", fmt.Errorf("短输入后处理未实现 (X len = %d)", len(X))
	}

	shuffled := encode3(X)
	raw := make([]byte, len(shuffled))
	for i := range shuffled {
		raw[i] = shuffled[i] ^ postXORConst[i]
	}
	return shuffledB64Encode(raw), nil
}

// ============================================
// URL 解析
// ============================================

// parseURLPath 提取 URL 路径部分（同 JS 中 e8 函数）。
func parseURLPath(url string) string {
	if !strings.HasPrefix(url, "http") {
		if !strings.HasPrefix(url, "/") {
			url = "/" + url
		}
		url = "https://www.zhihu.com" + url
	}

	schemeEnd := strings.Index(url, "://")
	if schemeEnd < 0 {
		return "/"
	}
	pathStart := strings.Index(url[schemeEnd+3:], "/")
	if pathStart >= 0 {
		return url[schemeEnd+3+pathStart:]
	}
	return "/"
}

// ============================================
// 签名生成
// ============================================

// SignatureResult 签名结果。
type SignatureResult struct {
	Source string `json:"source"`
	XZSE93 string `json:"x_zse_93"`
	XZSE96 string `json:"x_zse_96"`
}

// genSource 生成签名源字符串。
func genSource(url, body, zse93, dc0, xZst81 string) string {
	path := parseURLPath(url)

	parts := []string{zse93, path, dc0}

	if body != "" && len(body) <= 4096 {
		parts = append(parts, body)
	}

	if xZst81 != "" {
		parts = append(parts, xZst81)
	}

	return strings.Join(parts, "+")
}

// GetSignature 生成知乎签名。
func GetSignature(url, dc0, body string) (*SignatureResult, error) {
	zse93 := xzse93Version

	source := genSource(url, body, zse93, dc0, "")

	md5Hex := md5hex(source)

	signature, err := zhihuEncrypt(md5Hex)
	if err != nil {
		return nil, fmt.Errorf("zhihuEncrypt: %w", err)
	}

	return &SignatureResult{
		Source: source,
		XZSE93: zse93,
		XZSE96: xzse96Prefix + signature,
	}, nil
}

// md5hex 计算字符串的 MD5 hex 摘要。
func md5hex(s string) string {
	h := md5.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
