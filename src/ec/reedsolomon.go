package ec

import "errors"

// ReedSolomon 编码器/解码器。
// 使用 Cauchy 矩阵编码，保证任意 K×K 子矩阵可逆。
type ReedSolomon struct {
	gf           *GF256
	dataShards   int // K
	parityShards int // M
	totalShards  int // N = K + M
	encMatrix    [][]byte // (K+M) x K 编码矩阵
}

// NewReedSolomon 创建指定 K/M 参数的编解码器。
// dataShards: 数据分片数 K（必须 >= 1）
// parityShards: 校验分片数 M（必须 >= 1）
// K + M 不能超过 256（GF(2^8) 限制）。
func NewReedSolomon(dataShards, parityShards int) (*ReedSolomon, error) {
	if dataShards < 1 || parityShards < 1 {
		return nil, errors.New("reedsolomon: dataShards and parityShards must be >= 1")
	}
	if dataShards+parityShards > 256 {
		return nil, errors.New("reedsolomon: dataShards + parityShards must be <= 256")
	}

	gf := NewGF256()
	rs := &ReedSolomon{
		gf:           gf,
		dataShards:   dataShards,
		parityShards: parityShards,
		totalShards:  dataShards + parityShards,
	}

	// 构建 Cauchy 编码矩阵。
	rs.encMatrix = rs.buildCauchyMatrix()
	return rs, nil
}

// DataShards 返回数据分片数 K。
func (rs *ReedSolomon) DataShards() int {
	return rs.dataShards
}

// ParityShards 返回校验分片数 M。
func (rs *ReedSolomon) ParityShards() int {
	return rs.parityShards
}

// TotalShards 返回总分片数 N。
func (rs *ReedSolomon) TotalShards() int {
	return rs.totalShards
}

// Encode 将 data 编码为 K 个数据分片 + M 个校验分片。
// 返回长度为 N 的分片切片和每个分片的字节长度。
// 所有分片（包括数据分片）都经过 Cauchy 编码矩阵变换。
// data 会被 padding 到 K * shardSize 的整数倍。
func (rs *ReedSolomon) Encode(data []byte) ([][]byte, int) {
	shardSize := calcShardSize(len(data), rs.dataShards)
	if shardSize == 0 {
		shardSize = 1 // 空数据至少 1 字节
	}

	// Padding。
	padded := make([]byte, rs.dataShards*shardSize)
	copy(padded, data)

	// 拆分为 K 个原始数据块（临时，用于矩阵乘法）。
	dataBlocks := make([][]byte, rs.dataShards)
	for i := 0; i < rs.dataShards; i++ {
		dataBlocks[i] = padded[i*shardSize : (i+1)*shardSize]
	}

	// 对所有 N 个分片应用编码矩阵：shard[i] = sum(encMatrix[i][j] * dataBlock[j])
	shards := make([][]byte, rs.totalShards)
	for i := 0; i < rs.totalShards; i++ {
		shard := make([]byte, shardSize)
		for j := 0; j < rs.dataShards; j++ {
			coeff := rs.encMatrix[i][j]
			if coeff == 0 {
				continue
			}
			for k := 0; k < shardSize; k++ {
				shard[k] ^= rs.gf.Mul(coeff, dataBlocks[j][k])
			}
		}
		shards[i] = shard
	}

	return shards, shardSize
}

// Decode 从可用分片中恢复原始数据块。
// shards: 长度为 N 的分片切片，缺失分片为 nil。
// shardSize: 每个分片的字节长度。
// 返回时，shards[0..K-1] 会被填充为原始数据块（非编码后的分片）。
func (rs *ReedSolomon) Decode(shards [][]byte, shardSize int) error {
	// 收集可用分片的索引。
	var dataIndices, parityIndices []int
	for i := 0; i < rs.dataShards; i++ {
		if shards[i] != nil {
			dataIndices = append(dataIndices, i)
		}
	}
	for i := rs.dataShards; i < rs.totalShards; i++ {
		if shards[i] != nil {
			parityIndices = append(parityIndices, i)
		}
	}

	// 合并可用分片索引，取前 K 个。
	available := append(dataIndices, parityIndices...)
	if len(available) < rs.dataShards {
		return errors.New("reedsolomon: not enough shards to decode")
	}
	// 只取前 K 个可用分片。
	available = available[:rs.dataShards]

	// 构建 K×K 矩阵：行 = 可用分片对应的编码矩阵行，列 = 数据分片列。
	subMatrix := make([][]byte, rs.dataShards)
	for i := 0; i < rs.dataShards; i++ {
		subMatrix[i] = make([]byte, rs.dataShards)
		copy(subMatrix[i], rs.encMatrix[available[i]])
	}

	// 求逆矩阵。
	decMatrix, err := invertMatrix(subMatrix, rs.gf)
	if err != nil {
		return err
	}

	// 用逆矩阵恢复所有 K 个原始数据块。
	// data_block[j] = sum_i(decMatrix[j][i] * shard[available[i]])
	dataBlocks := make([][]byte, rs.dataShards)
	for j := 0; j < rs.dataShards; j++ {
		block := make([]byte, shardSize)
		for i := 0; i < rs.dataShards; i++ {
			coeff := decMatrix[j][i]
			if coeff == 0 {
				continue
			}
			srcShard := shards[available[i]]
			for k := 0; k < shardSize; k++ {
				block[k] ^= rs.gf.Mul(coeff, srcShard[k])
			}
		}
		dataBlocks[j] = block
	}

	// 将恢复的数据块写回 shards[0..K-1]。
	for j := 0; j < rs.dataShards; j++ {
		shards[j] = dataBlocks[j]
	}

	return nil
}

// Reconstruct 从可用分片中恢复所有 N 个分片（包括 parity）。
// 与 Decode 不同，Reconstruct 会填充 shards 中的所有 nil 位置。
func (rs *ReedSolomon) Reconstruct(shards [][]byte, shardSize int) error {
	if err := rs.Decode(shards, shardSize); err != nil {
		return err
	}
	// 用恢复的数据分片重新编码缺失的 parity 分片。
	for i := rs.dataShards; i < rs.totalShards; i++ {
		if shards[i] != nil {
			continue
		}
		shard := make([]byte, shardSize)
		for j := 0; j < rs.dataShards; j++ {
			coeff := rs.encMatrix[i][j]
			if coeff == 0 {
				continue
			}
			for k := 0; k < shardSize; k++ {
				shard[k] ^= rs.gf.Mul(coeff, shards[j][k])
			}
		}
		shards[i] = shard
	}
	return nil
}

// buildCauchyMatrix 构建 N×K 的 Cauchy 编码矩阵。
// 选取两个不相交的集合 X（大小 N）和 Y（大小 K），
// C[i][j] = 1 / (X[i] XOR Y[j])。
// Cauchy 矩阵保证任意 K×K 子矩阵可逆。
func (rs *ReedSolomon) buildCauchyMatrix() [][]byte {
	n := rs.totalShards
	k := rs.dataShards

	// 选取不相交的集合 X 和 Y。
	// X = {1, 2, ..., N}，Y = {N+1, N+2, ..., N+K}。
	// 这保证了 X ∩ Y = ∅。
	matrix := make([][]byte, n)
	for i := 0; i < n; i++ {
		matrix[i] = make([]byte, k)
		x := byte(i + 1)
		for j := 0; j < k; j++ {
			y := byte(n + j + 1)
			denom := x ^ y
			matrix[i][j] = rs.gf.Inv(denom)
		}
	}
	return matrix
}

// calcShardSize 计算分片大小。
func calcShardSize(dataLen, dataShards int) int {
	if dataLen == 0 {
		return 0
	}
	shardSize := dataLen / dataShards
	if dataLen%dataShards != 0 {
		shardSize++
	}
	return shardSize
}

// invertMatrix 对 GF(2^8) 上的方阵求逆，返回逆矩阵。
// 使用增广矩阵 [A | I] 高斯-约当消元法。
func invertMatrix(a [][]byte, gf *GF256) ([][]byte, error) {
	n := len(a)

	// 构建增广矩阵 [A | I]。
	aug := make([][]byte, n)
	for i := 0; i < n; i++ {
		aug[i] = make([]byte, 2*n)
		copy(aug[i], a[i])
		aug[i][n+i] = 1 // 单位矩阵部分
	}

	// 高斯-约当消元。
	for col := 0; col < n; col++ {
		// 找主元。
		pivot := -1
		for row := col; row < n; row++ {
			if aug[row][col] != 0 {
				pivot = row
				break
			}
		}
		if pivot == -1 {
			return nil, errors.New("reedsolomon: matrix is singular")
		}

		// 交换行。
		if pivot != col {
			aug[col], aug[pivot] = aug[pivot], aug[col]
		}

		// 主元归一。
		invPivot := gf.Inv(aug[col][col])
		for j := 0; j < 2*n; j++ {
			aug[col][j] = gf.Mul(aug[col][j], invPivot)
		}

		// 消去其他行的当前列。
		for row := 0; row < n; row++ {
			if row == col {
				continue
			}
			factor := aug[row][col]
			if factor == 0 {
				continue
			}
			for j := 0; j < 2*n; j++ {
				aug[row][j] = gf.Add(aug[row][j], gf.Mul(factor, aug[col][j]))
			}
		}
	}

	// 提取逆矩阵（右半部分）。
	inv := make([][]byte, n)
	for i := 0; i < n; i++ {
		inv[i] = make([]byte, n)
		copy(inv[i], aug[i][n:])
	}
	return inv, nil
}

// gaussJordan 对 GF(2^8) 上的方阵进行高斯-约当消元，原地求逆。
// 保留此函数用于测试中的可逆性检查。
func gaussJordan(matrix [][]byte, gf *GF256) error {
	inv, err := invertMatrix(matrix, gf)
	if err != nil {
		return err
	}
	for i := range matrix {
		copy(matrix[i], inv[i])
	}
	return nil
}
