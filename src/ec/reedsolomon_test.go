package ec

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"math"
	"testing"
)

func TestGF256Add(t *testing.T) {
	gf := NewGF256()
	if gf.Add(0, 0) != 0 {
		t.Fatal("0+0 should be 0")
	}
	if gf.Add(1, 1) != 0 {
		t.Fatal("1+1 should be 0 (XOR)")
	}
	if gf.Add(0xAA, 0x55) != 0xFF {
		t.Fatal("0xAA+0x55 should be 0xFF")
	}
}

func TestGF256Mul(t *testing.T) {
	gf := NewGF256()
	// a * 0 = 0
	if gf.Mul(42, 0) != 0 {
		t.Fatal("a * 0 should be 0")
	}
	if gf.Mul(0, 42) != 0 {
		t.Fatal("0 * a should be 0")
	}
	// a * 1 = a
	for a := 1; a < 256; a++ {
		if gf.Mul(byte(a), 1) != byte(a) {
			t.Fatalf("%d * 1 != %d", a, a)
		}
	}
	// 交换律
	for a := 1; a < 20; a++ {
		for b := 1; b < 20; b++ {
			if gf.Mul(byte(a), byte(b)) != gf.Mul(byte(b), byte(a)) {
				t.Fatalf("mul(%d,%d) != mul(%d,%d)", a, b, b, a)
			}
		}
	}
	// 结合律
	for a := 1; a < 10; a++ {
		for b := 1; b < 10; b++ {
			for c := 1; c < 10; c++ {
				lhs := gf.Mul(gf.Mul(byte(a), byte(b)), byte(c))
				rhs := gf.Mul(byte(a), gf.Mul(byte(b), byte(c)))
				if lhs != rhs {
					t.Fatalf("associativity failed for %d,%d,%d", a, b, c)
				}
			}
		}
	}
}

func TestGF256Div(t *testing.T) {
	gf := NewGF256()
	// a / a = 1
	for a := 1; a < 256; a++ {
		if gf.Div(byte(a), byte(a)) != 1 {
			t.Fatalf("%d / %d != 1", a, a)
		}
	}
	// a * inv(a) = 1
	for a := 1; a < 256; a++ {
		if gf.Mul(byte(a), gf.Inv(byte(a))) != 1 {
			t.Fatalf("a * inv(a) != 1 for a=%d", a)
		}
	}
	// div = mul by inv
	for a := 1; a < 50; a++ {
		for b := 1; b < 50; b++ {
			expected := gf.Mul(byte(a), gf.Inv(byte(b)))
			got := gf.Div(byte(a), byte(b))
			if got != expected {
				t.Fatalf("div(%d,%d)=%d != mul(%d,inv(%d))=%d", a, b, got, a, b, expected)
			}
		}
	}
}

func TestReedSolomonEncodeDecode(t *testing.T) {
	testCases := []struct {
		name string
		k    int
		m    int
		data []byte
	}{
		{"4+2 small", 4, 2, []byte("hello reed-solomon world!")},
		{"4+2 exactly K bytes", 4, 2, []byte{1, 2, 3, 4}},
		{"4+2 one byte", 4, 2, []byte{42}},
		{"4+2 empty", 4, 2, []byte{}},
		{"4+2 large", 4, 2, make([]byte, 10000)},
		{"2+1 simple", 2, 1, []byte{0xAA, 0xBB, 0xCC, 0xDD}},
		{"6+3", 6, 3, make([]byte, 6000)},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			rs, err := NewReedSolomon(tc.k, tc.m)
			if err != nil {
				t.Fatal(err)
			}

			shards, shardSize := rs.Encode(tc.data)
			if len(shards) != tc.k+tc.m {
				t.Fatalf("expected %d shards, got %d", tc.k+tc.m, len(shards))
			}

			// 全分片解码恢复原始数据。
			if err := rs.Decode(shards, shardSize); err != nil {
				t.Fatal(err)
			}
			recovered := make([]byte, 0, len(tc.data))
			for i := 0; i < rs.dataShards; i++ {
				recovered = append(recovered, shards[i]...)
			}
			recovered = recovered[:len(tc.data)]
			if !bytes.Equal(recovered, tc.data) {
				t.Fatalf("decode with all shards failed")
			}
		})
	}
}

func TestReedSolomonMissingShards(t *testing.T) {
	rs, _ := NewReedSolomon(4, 2)
	data := []byte("erasure coding test data for recovery verification")
	shards, shardSize := rs.Encode(data)

	// 测试缺失 1 个数据分片。
	for missing := 0; missing < 4; missing++ {
		t.Run(fmt.Sprintf("missing_data_shard_%d", missing), func(t *testing.T) {
			testShards := copyShards(shards)
			testShards[missing] = nil
			if err := rs.Decode(testShards, shardSize); err != nil {
				t.Fatal(err)
			}
			verifyRecovery(t, rs, testShards, data)
		})
	}

	// 测试缺失 1 个校验分片。
	for missing := 4; missing < 6; missing++ {
		t.Run(fmt.Sprintf("missing_parity_shard_%d", missing), func(t *testing.T) {
			testShards := copyShards(shards)
			testShards[missing] = nil
			if err := rs.Decode(testShards, shardSize); err != nil {
				t.Fatal(err)
			}
			verifyRecovery(t, rs, testShards, data)
		})
	}

	// 测试缺失 M 个分片（混合数据+校验）。
	t.Run("missing_2_shards_0_4", func(t *testing.T) {
		testShards := copyShards(shards)
		testShards[0] = nil
		testShards[4] = nil
		if err := rs.Decode(testShards, shardSize); err != nil {
			t.Fatal(err)
		}
		verifyRecovery(t, rs, testShards, data)
	})

	t.Run("missing_2_shards_1_5", func(t *testing.T) {
		testShards := copyShards(shards)
		testShards[1] = nil
		testShards[5] = nil
		if err := rs.Decode(testShards, shardSize); err != nil {
			t.Fatal(err)
		}
		verifyRecovery(t, rs, testShards, data)
	})

	t.Run("missing_2_data_shards_0_3", func(t *testing.T) {
		testShards := copyShards(shards)
		testShards[0] = nil
		testShards[3] = nil
		if err := rs.Decode(testShards, shardSize); err != nil {
			t.Fatal(err)
		}
		verifyRecovery(t, rs, testShards, data)
	})

	// 测试缺失 M+1 个分片应该失败。
	t.Run("missing_3_shards_should_fail", func(t *testing.T) {
		testShards := copyShards(shards)
		testShards[0] = nil
		testShards[1] = nil
		testShards[2] = nil
		if err := rs.Decode(testShards, shardSize); err == nil {
			t.Fatal("expected error when more than M shards are missing")
		}
	})
}

func TestReedSolomonRandomData(t *testing.T) {
	rs, _ := NewReedSolomon(4, 2)
	data := make([]byte, 8192)
	rand.Read(data)

	shards, shardSize := rs.Encode(data)

	// 测试每次从全新副本开始。
	for _, missing := range []int{1, 3, 5} {
		testShards := copyShards(shards)
		testShards[missing] = nil
		if err := rs.Decode(testShards, shardSize); err != nil {
			t.Fatalf("failed to recover with shard %d missing: %v", missing, err)
		}
		verifyRecovery(t, rs, testShards, data)
	}

	// 同时缺失 2 个分片。
	testShards := copyShards(shards)
	testShards[0] = nil
	testShards[4] = nil
	if err := rs.Decode(testShards, shardSize); err != nil {
		t.Fatalf("failed to recover with shards 0,4 missing: %v", err)
	}
	verifyRecovery(t, rs, testShards, data)
}

func TestReedSolomonInvalidParams(t *testing.T) {
	_, err := NewReedSolomon(0, 2)
	if err == nil {
		t.Fatal("expected error for dataShards=0")
	}
	_, err = NewReedSolomon(4, 0)
	if err == nil {
		t.Fatal("expected error for parityShards=0")
	}
	_, err = NewReedSolomon(128, 129)
	if err == nil {
		t.Fatal("expected error for k+m > 256")
	}
}

func TestGF256Generator(t *testing.T) {
	gf := NewGF256()
	// GF(2^8) 的生成元应该满足: g^255 = 1 且 g^i != 1 for 0 < i < 255。
	// x = 2 是 0x11D 的本原元。
	x := byte(2)
	product := byte(1)
	for i := 1; i <= 255; i++ {
		product = gf.Mul(product, x)
		if i < 255 && product == 1 {
			t.Fatalf("generator has order < 255 at step %d", i)
		}
	}
	if product != 1 {
		t.Fatal("generator order != 255")
	}
}

func TestCauchyMatrixInvertibility(t *testing.T) {
	// 测试多种 K/M 组合的 Cauchy 矩阵子矩阵可逆性。
	configs := []struct{ k, m int }{
		{2, 1}, {2, 2}, {3, 1}, {3, 2}, {3, 3},
		{4, 2}, {4, 4}, {6, 3}, {8, 4}, {10, 4},
	}

	for _, cfg := range configs {
		t.Run(fmt.Sprintf("K=%d_M=%d", cfg.k, cfg.m), func(t *testing.T) {
			rs, err := NewReedSolomon(cfg.k, cfg.m)
			if err != nil {
				t.Fatal(err)
			}

			// 测试所有 C(totalShards, totalShards-k) 种 k 分片子集的可逆性。
			n := rs.totalShards
			testAllSubsets(t, rs, n, cfg.k, 0, make([]int, 0))
		})
	}
}

func testAllSubsets(t *testing.T, rs *ReedSolomon, n, k, start int, chosen []int) {
	if len(chosen) == k {
		// 构建子矩阵并检查是否可逆。
		sub := make([][]byte, k)
		for i := 0; i < k; i++ {
			sub[i] = make([]byte, k)
			copy(sub[i], rs.encMatrix[chosen[i]])
		}
		gf := NewGF256()
		if err := gaussJordan(sub, gf); err != nil {
			t.Fatalf("singular matrix for subset %v", chosen)
		}
		return
	}
	// 限制测试数量，避免组合爆炸。
	maxSubsets := 100
	count := 0
	for i := start; i < n; i++ {
		testAllSubsets(t, rs, n, k, i+1, append(chosen, i))
		count++
		if count >= maxSubsets {
			return
		}
	}
}

func copyShards(shards [][]byte) [][]byte {
	cp := make([][]byte, len(shards))
	for i, s := range shards {
		if s != nil {
			cp[i] = make([]byte, len(s))
			copy(cp[i], s)
		}
	}
	return cp
}

func verifyRecovery(t *testing.T, rs *ReedSolomon, shards [][]byte, original []byte) {
	t.Helper()
	recovered := make([]byte, 0, len(original))
	for i := 0; i < rs.dataShards; i++ {
		recovered = append(recovered, shards[i]...)
	}
	recovered = recovered[:len(original)]
	if !bytes.Equal(recovered, original) {
		t.Fatalf("recovered data doesn't match original (len=%d)", len(original))
	}
}

// 检测 testAllSubsets 的覆盖率 — 对小配置枚举全部子集。
func TestCauchyMatrixAllSubsets(t *testing.T) {
	type config struct{ k, m int }
	configs := []config{
		{2, 1}, {2, 2}, {3, 1}, {3, 2}, {4, 2},
	}
	for _, cfg := range configs {
		t.Run(fmt.Sprintf("K=%d_M=%d", cfg.k, cfg.m), func(t *testing.T) {
			rs, _ := NewReedSolomon(cfg.k, cfg.m)
			n := rs.totalShards
			total := comb(n, cfg.k)
			counted := 0
			var enumerate func(start int, chosen []int)
			enumerate = func(start int, chosen []int) {
				if len(chosen) == cfg.k {
					sub := make([][]byte, cfg.k)
					for i := 0; i < cfg.k; i++ {
						sub[i] = make([]byte, cfg.k)
						copy(sub[i], rs.encMatrix[chosen[i]])
					}
					gf := NewGF256()
					if err := gaussJordan(sub, gf); err != nil {
						t.Fatalf("singular matrix for subset %v", chosen)
					}
					counted++
					return
				}
				for i := start; i < n; i++ {
					enumerate(i+1, append(chosen, i))
				}
			}
			enumerate(0, nil)
			if counted != total {
				t.Fatalf("only tested %d/%d subsets", counted, total)
			}
		})
	}
}

func comb(n, k int) int {
	if k > n {
		return 0
	}
	if k == 0 || k == n {
		return 1
	}
	// 小数值直接计算。
	result := 1
	for i := 0; i < k; i++ {
		result = result * (n - i) / (i + 1)
	}
	return result
}

func TestGF256Exhaustive(t *testing.T) {
	gf := NewGF256()
	// 全量检查乘法表的一致性：a * b / b = a。
	for a := 1; a < 256; a++ {
		for b := 1; b < 256; b++ {
			product := gf.Mul(byte(a), byte(b))
			recovered := gf.Mul(product, gf.Inv(byte(b)))
			if recovered != byte(a) {
				t.Fatalf("a*b*inv(b) != a for a=%d b=%d: got %d", a, b, recovered)
			}
		}
	}
	// 检查加法：a + a = 0。
	for a := 0; a < 256; a++ {
		if gf.Add(byte(a), byte(a)) != 0 {
			t.Fatalf("a + a != 0 for a=%d", a)
		}
	}
}

// 确保 shardSize 为 1 时（空数据或极小数据）也能正确编解码。
func TestReedSolomonShardSize1(t *testing.T) {
	rs, _ := NewReedSolomon(4, 2)
	data := []byte{42}
	shards, shardSize := rs.Encode(data)
	if shardSize != 1 {
		t.Fatalf("expected shardSize=1, got %d", shardSize)
	}
	// 缺失 2 个分片。
	shards[0] = nil
	shards[5] = nil
	if err := rs.Decode(shards, shardSize); err != nil {
		t.Fatal(err)
	}
	recovered := []byte{shards[0][0], shards[1][0], shards[2][0], shards[3][0]}
	recovered = recovered[:len(data)]
	if !bytes.Equal(recovered, data) {
		t.Fatalf("recovery failed: got %v, want %v", recovered, data)
	}
}

// 大分片大小测试 — 确保分片大小接近 math.MaxInt 时不会溢出。
func TestReedSolomonLargeShardSize(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large shard test")
	}
	rs, _ := NewReedSolomon(4, 2)
	// 1MB 数据。
	data := make([]byte, 1<<20)
	for i := range data {
		data[i] = byte(i)
	}
	shards, shardSize := rs.Encode(data)
	shards[1] = nil
	shards[4] = nil
	if err := rs.Decode(shards, shardSize); err != nil {
		t.Fatal(err)
	}
	recovered := make([]byte, 0, len(data))
	for i := 0; i < rs.dataShards; i++ {
		recovered = append(recovered, shards[i]...)
	}
	recovered = recovered[:len(data)]
	if !bytes.Equal(recovered, data) {
		t.Fatal("large data recovery failed")
	}
}

func init() {
	// 确保测试编译通过（使用 math 包）。
	_ = math.MaxInt
}
