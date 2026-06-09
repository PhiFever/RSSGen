package zhihu

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ============================================
// SM4 块加密测试（golden 向量来自 sm4_block_vectors.json）
// ============================================

func TestSM4EncryptBlock(t *testing.T) {
	// 从 sm4_block_vectors.json 加载测试向量
	vectors := loadSM4BlockVectors(t)
	if len(vectors) == 0 {
		t.Fatal("未加载到 SM4 块加密测试向量")
	}

	for i, v := range vectors {
		input := byteArrayFromInts(v.Input)
		expected := byteArrayFromInts(v.Output)
		result := SM4EncryptBlock(input)

		for j := 0; j < 16; j++ {
			if result[j] != expected[j] {
				t.Errorf("SM4 块加密向量 %d: 字节 %d 期望 %d, 实际 %d", i, j, expected[j], result[j])
				break
			}
		}
	}
}

// ============================================
// 加密端到端测试（golden 向量来自 encrypt_corpus.json 的 32 字节输入）
// ============================================

func TestEncryptWithFixedRandom(t *testing.T) {
	samples := loadEncryptCorpus32(t)
	if len(samples) == 0 {
		t.Fatal("未加载到 32 字节加密测试向量")
	}

	for i, s := range samples {
		randomByte := byte(s.RCalls[0].Input[0])
		result, err := EncryptWithFixedRandom(s.Input, randomByte)
		if err != nil {
			t.Errorf("加密向量 %d (input=%s): 错误 %v", i, s.Input[:16], err)
			continue
		}
		if result != s.B64 {
			t.Errorf("加密向量 %d (input=%s):\n  期望: %s\n  实际: %s", i, s.Input[:16], s.B64, result)
		}
	}
}

// ============================================
// 签名生成测试
// ============================================

func TestGetSignature(t *testing.T) {
	// 测试 1: 基本签名格式
	sig, err := GetSignature(
		"https://www.zhihu.com/api/v4/questions/123/answers?limit=5",
		"test_dc0_value",
		"",
	)
	if err != nil {
		t.Fatalf("GetSignature 错误: %v", err)
	}

	if sig.XZSE93 != XZSE93Version {
		t.Errorf("XZSE93: 期望 %s, 实际 %s", XZSE93Version, sig.XZSE93)
	}
	if !strings.HasPrefix(sig.XZSE96, XZSE96Prefix) {
		t.Errorf("XZSE96 应以 %s 开头, 实际: %s", XZSE96Prefix, sig.XZSE96)
	}
	if len(sig.XZSE96) != 68 {
		t.Errorf("XZSE96 长度应为 68, 实际: %d", len(sig.XZSE96))
	}

	// 测试 2: source 字符串正确性
	expectedSource := "101_3_3.0+/api/v4/questions/123/answers?limit=5+test_dc0_value"
	if sig.Source != expectedSource {
		t.Errorf("Source:\n  期望: %s\n  实际: %s", expectedSource, sig.Source)
	}

	// 测试 3: 不同 URL 产生不同签名
	sig2, err := GetSignature(
		"https://www.zhihu.com/api/v4/questions/222/answers",
		"test_dc0_value",
		"",
	)
	if err != nil {
		t.Fatalf("GetSignature 2 错误: %v", err)
	}
	if sig.XZSE96 == sig2.XZSE96 {
		t.Error("不同 URL 应产生不同签名")
	}

	// 测试 4: 带 body 的签名
	sig3, err := GetSignature(
		"https://www.zhihu.com/api/v4/members/user123/activities",
		"abcdef123456=|1700000000",
		`{"action":"next"}`,
	)
	if err != nil {
		t.Fatalf("GetSignature 3 错误: %v", err)
	}
	expectedSource3 := `101_3_3.0+/api/v4/members/user123/activities+abcdef123456=|1700000000+{"action":"next"}`
	if sig3.Source != expectedSource3 {
		t.Errorf("Source 3:\n  期望: %s\n  实际: %s", expectedSource3, sig3.Source)
	}
}

// ============================================
// 辅助函数
// ============================================

type sm4BlockVector struct {
	Input  []int `json:"input"`
	Output []int `json:"output"`
}

func loadSM4BlockVectors(t *testing.T) []sm4BlockVector {
	t.Helper()
	path := filepath.Join("testdata", "sm4_block_vectors.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 sm4_block_vectors.json: %v", err)
	}
	var vectors []sm4BlockVector
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatalf("解析 sm4_block_vectors.json: %v", err)
	}
	return vectors
}

type rCall struct {
	Input []int `json:"input"`
}

type encryptCorpusEntry struct {
	Input  string  `json:"input"`
	B64    string  `json:"b64"`
	Raw    []int   `json:"raw"`
	RCalls []rCall `json:"r_calls"`
}

func loadEncryptCorpus32(t *testing.T) []encryptCorpusEntry {
	t.Helper()
	path := filepath.Join("testdata", "encrypt_corpus.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 encrypt_corpus.json: %v", err)
	}
	var corpus []encryptCorpusEntry
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatalf("解析 encrypt_corpus.json: %v", err)
	}

	var samples32 []encryptCorpusEntry
	for _, e := range corpus {
		if len(e.Input) == 32 {
			samples32 = append(samples32, e)
		}
	}
	return samples32
}

func byteArrayFromInts(ints []int) []byte {
	b := make([]byte, len(ints))
	for i, v := range ints {
		b[i] = byte(v)
	}
	return b
}
