<!-- tags: storage, distributed, error-correction, encoding -->
# 纠删码（Erasure Coding）

## 概述

**纠删码**是一种数据冗余技术。与"完整复制"不同，纠删码将数据编码为若干分片，只需要一部分分片就能恢复原始数据。

**直觉理解**：想象你有一份文件，切成 K 块，然后通过数学运算生成额外的 M 块校验数据。只要拿到任意 K 块（无论是原始数据块还是校验块），就能完整恢复文件。

## 1. 对比：复制 vs 纠删码

| 方案 | 3 副本 | EC 4+2（本项目的典型配置） |
|------|--------|--------------------------|
| 存储开销 | 3x | 1.5x（6 份分片，原始 4 份） |
| 可容忍故障 | 2 个副本丢失 | 2 个分片丢失 |
| 编码开销 | 无 | 写入时需要计算校验分片 |
| 读取开销 | 读任意一个副本 | 需要读 K 个分片并解码 |

纠删码的核心优势是**存储效率**：用更少的磁盘空间实现相同级别的容错能力。

## 2. 有限域算术 GF(2^8)

纠删码的运算不是普通加减乘除，而是在**有限域 GF(2^8)**（也叫伽罗瓦域）上进行的。

### 什么是有限域

普通算术中，1+1=2，除法结果可以是小数。GF(2^8) 中：
- 所有元素是 0~255 的字节
- 加法和减法是 **XOR**（异或）：`a + b = a XOR b`
- 乘法和除法通过预计算的查找表完成
- 所有运算的结果仍然是 0~255 的字节，不会溢出

### 为什么用 GF(2^8)

- 数据的本质就是字节（0~255），直接在字节层面运算，无需类型转换
- XOR 运算在 CPU 上极快
- 有成熟的数学理论保证编码和解码的正确性

### 查找表优化

本项目预计算两张表，将乘除法变成查表操作：

```go
// src/ec/galois.go
type GF256 struct {
    expTable [512]byte // 指数表：exp[i] = 2^i
    logTable [256]byte // 对数表：log[a] = i，其中 2^i = a
}

// 乘法：a × b = exp[log[a] + log[b]]
func (gf *GF256) Mul(a, b byte) byte {
    if a == 0 || b == 0 { return 0 }
    return gf.expTable[int(gf.logTable[a]) + int(gf.logTable[b])]
}

// 除法：a ÷ b = exp[log[a] - log[b]]
func (gf *GF256) Div(a, b byte) byte {
    if a == 0 { return 0 }
    return gf.expTable[int(gf.logTable[a]) - int(gf.logTable[b]) + 255]
}
```

初始化时使用不可约多项式 `x^8 + x^4 + x^3 + x^2 + 1`（0x11D），这与 RAID-6 和标准 Reed-Solomon 使用相同的参数。

## 3. Reed-Solomon 编解码

### 编码过程

将原始数据编码为 K 个数据分片 + M 个校验分片，总共 N = K+M 个分片：

```
原始数据: [D0, D1, D2, D3]    (K=4)
校验数据: [P0, P1]            (M=2)
总分片:   [S0, S1, S2, S3, S4, S5]  (N=6)

编码矩阵（6×4）× 原始数据向量（4×1）= 分片向量（6×1）
```

本项目使用 **Cauchy 矩阵**作为编码矩阵，因为它保证任意 K×K 子矩阵都可逆——这意味着只要有任意 K 个分片就能恢复原始数据。

```go
func (rs *ReedSolomon) Encode(data []byte) ([][]byte, int) {
    shardSize := calcShardSize(len(data), rs.dataShards)
    padded := make([]byte, rs.dataShards*shardSize)
    copy(padded, data)

    shards := make([][]byte, rs.totalShards)
    for i := 0; i < rs.totalShards; i++ {
        shard := make([]byte, shardSize)
        for j := 0; j < rs.dataShards; j++ {
            coeff := rs.encMatrix[i][j]
            if coeff == 0 { continue }
            for k := 0; k < shardSize; k++ {
                shard[k] ^= rs.gf.Mul(coeff, dataBlocks[j][k])
            }
        }
        shards[i] = shard
    }
    return shards, shardSize
}
```

核心操作是**矩阵乘法**，但乘法替换为 GF(2^8) 乘法，加法替换为 XOR。

### 解码过程

```
丢失分片 S2, S4 → shards = [S0, S1, nil, S3, nil, S5]

1. 取出 K=4 个可用分片：S0, S1, S3, S5
2. 从编码矩阵中取出对应行，构建 4×4 子矩阵
3. 对子矩阵求逆
4. 逆矩阵 × 可用分片 = 原始数据
5. 用编码矩阵重新计算丢失的分片 S2, S4
```

```go
func (rs *ReedSolomon) Decode(shards [][]byte, shardSize int) error {
    // 收集 K 个可用分片的索引
    available := append(dataIndices, parityIndices...)[:rs.dataShards]

    // 构建子矩阵并求逆
    decMatrix, _ := invertMatrix(subMatrix, rs.gf)

    // 恢复原始数据
    for j := 0; j < rs.dataShards; j++ {
        block[k] ^= rs.gf.Mul(decMatrix[j][i], srcShard[k])
    }
}
```

矩阵求逆使用**高斯-约当消元法**，在 GF(2^8) 上操作。

## 4. 自修复（Self-Repair）

本项目在读取数据时，如果发现有分片缺失但可以解码，会自动修复缺失的分片：

```go
// src/storage/ec.go
func (eb *ECBackend) GetObject(bucket, key string) ([]byte, *ObjectMeta, error) {
    // 1. 从可用磁盘读取分片（缺失的标记为 nil）
    // 2. 检查可用分片数 >= K
    // 3. Decode 恢复原始数据
    // 4. 自修复：将恢复的分片写回故障磁盘
    if needsRepair {
        eb.repairShards(bucket, key, shards, missingIndices, ecMeta)
    }
}
```

修复是静默的——对用户透明，不影响读取结果。

## 5. 磁盘布局

```
disk-0/{bucket}/{key}    ← 分片 0（数据）
disk-1/{bucket}/{key}    ← 分片 1（数据）
disk-2/{bucket}/{key}    ← 分片 2（数据）
disk-3/{bucket}/{key}    ← 分片 3（数据）
disk-4/{bucket}/{key}    ← 分片 4（校验）
disk-5/{bucket}/{key}    ← 分片 5（校验）
meta-root/{bucket}/{key}.ec-meta  ← EC 元数据（独立存储）
```

EC 元数据独立存储在一个可靠的磁盘上，记录原始大小、分片大小、K/M 参数等，是解码的前提条件。

## 6. 本项目中的典型配置

| 参数 | 默认值 | 说明 |
|------|--------|------|
| K（数据分片） | 4 | 原始数据被切成 4 块 |
| M（校验分片） | 2 | 额外生成 2 块校验数据 |
| N（总分片） | 6 | 需要 6 个磁盘 |
| 可容忍故障 | 2 个磁盘 | 丢失 2 个分片仍可恢复 |
| 存储开销 | 1.5x | 比三副本节省一半空间 |

## 7. 开源项目中的使用

| 项目 | 说明 |
|------|------|
| **Ceph** | 使用 Jerasure/ISA-L 库实现 EC，支持多种编码策略 |
| **HDFS** | Hadoop 3.x 支持 Reed-Solomon 和 XOR 编码的 EC |
| **MinIO** | 支持 Reed-Solomon 纠删码，与本项目相同的思路 |
| **WhatsApp** | 使用纠删码存储消息和媒体文件 |
| **Facebook f4** | 冷存储使用 EC 降低存储成本 |
| **klauspost/reedsolomon** | Go 生态最流行的纠删码库，本项目独立实现 |

纠删码是现代存储系统的核心技术之一，尤其在冷存储和大规模对象存储场景中不可或缺。
