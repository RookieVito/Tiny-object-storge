<!-- tags: phase-summary -->
# Phase 5 完成总结

## 1. 完成状态：全部完成

Phase 5 新增 5 个文件（galois.go、reedsolomon.go、reedsolomon_test.go、ec.go、test/phase5.go），
修改 3 个文件（config.go、main.go、error.go），新增 45 个 EC 单元测试和 17 个集成测试全部通过，Phase 1-4 全量回归零回归。

---

## 2. Phase 5 实现内容

### 2.1 GF(2^8) 有限域算术（ec/galois.go）

纯标准库实现，基于不可约多项式 0x11D（与 RAID-6 / Cauchy Reed-Solomon 一致）：

- 预计算 exp 表（512 字节，2x 循环避免取模）和 log 表
- `Add`/`Sub`（XOR）、`Mul`/`Div`/`Inv`（查找表）
- 零依赖，零外部包

### 2.2 Cauchy Reed-Solomon 编解码器（ec/reedsolomon.go）

- **Cauchy 矩阵编码**：C[i][j] = 1 / (X[i] XOR Y[j])，保证任意 K×K 子矩阵可逆
- **Encode**：数据 padding → 拆分 K 数据块 → 全部 N 个分片经过编码矩阵变换
- **Decode**：选取 K 个可用分片 → 构建子矩阵 → 增广矩阵高斯-约当消元求逆 → 恢复所有原始数据块
- 支持任意 K≥1、M≥1、K+M≤256

### 2.3 ECBackend（storage/ec.go）

实现 `StorageBackend` 接口的纠删码后端：

- **PutObject**：Reed-Solomon 编码 → N 个分片写入 N 个磁盘 → ECObjectMeta 写入独立 metaStore
- **GetObject**：读 ECObjectMeta → 从可用磁盘读分片 → Reed-Solomon 解码 → 截断到原始大小
- **降级读**：可用磁盘 >= K 时正常解码，< K 时返回 503 InsufficientStorage
- **Bucket 操作**：在所有磁盘 + metaStore 同步执行
- **ListObjects**：遍历 metaStore 中的 .ec-meta 文件，解析后应用 prefix/delimiter/pagination
- **SetDiskState**：支持手动标记磁盘上下线（用于测试和管理）

### 2.4 ECObjectMeta

```json
{
  "key": "photos/cat.jpg",
  "original_size": 102400,
  "shard_size": 25601,
  "data_shards": 4,
  "parity_shards": 2,
  "etag": "\"...\"",
  "content_type": "image/jpeg",
  "last_modified": "2026-04-20T...",
  "user_metadata": {}
}
```

### 2.5 配置和启动

新增 `ECConfig` 结构体：

```json
{
  "backend_type": "ec",
  "ec": {
    "disks": ["./disk-0", "./disk-1", "./disk-2", "./disk-3", "./disk-4", "./disk-5"],
    "data_shards": 4,
    "parity_shards": 2,
    "meta_root": "./ec-meta"
  }
}
```

- 启动时校验磁盘数量 >= K+M，自动创建磁盘目录
- 默认 K=4, M=2

### 2.6 磁盘布局

```
disk-0/{bucket}/{key}           # 分片 0（编码后数据）
disk-1/{bucket}/{key}           # 分片 1
disk-2/{bucket}/{key}           # 分片 2
disk-3/{bucket}/{key}           # 分片 3
disk-4/{bucket}/{key}           # 分片 4（校验）
disk-5/{bucket}/{key}           # 分片 5（校验）

ec-meta/{bucket}/{key}.ec-meta  # ECObjectMeta JSON（独立存储）
```

---

## 3. 依赖关系

```
ec/        ← 新增包，无依赖（纯数学）
storage/   ← 新增依赖 ec（ECBackend 使用 ReedSolomon）
```

依赖图保持无环。

---

## 4. 测试覆盖

**EC 单元测试（go test ./src/ec/...）：45 个**
- GF(2^8) 运算正确性（加法、乘法、除法、逆元、交换律、结合律）
- 全量检查 mul(a)*inv(b) == a
- 全分片可用编解码
- 缺失 1 个数据分片解码
- 缺失 1 个校验分片解码
- 缺失 2 个混合分片解码
- 缺失超过 M 个分片返回错误
- 随机数据（8KB）编码解码
- Cauchy 矩阵子矩阵可逆性验证
- shardSize=1 边界情况
- 1MB 大数据测试

**Phase 5 集成测试（HTTP 级别）：17 个**
- 基本 Put/Get 往返
- HeadObject 验证
- 嵌套 key
- 大对象（16KB）
- Delete 和 NoSuchKey
- ListBuckets 和 ListObjects
- 降级读（1 个磁盘故障）
