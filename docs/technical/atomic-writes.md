# 原子写入与文件系统安全

## 概述

对象存储的核心要求之一是**数据完整性**：写入过程中系统崩溃或断电，绝不能
留下损坏的半成品文件。本项目使用"临时文件 + Rename"模式实现原子写入。

## 1. 问题：非原子写入

简单直接写入目标文件存在损坏风险：

```
状态 1: 创建文件 ───→ 状态 2: 写入一半 ──✗ 崩溃 ──→ 损坏文件
```

读取进程可能读到截断的数据或校验失败的文件。

## 2. 解决方案：Temp + Rename 模式

```
状态 1: 创建临时文件 ─→ 状态 2: 写入完成 ─→ 状态 3: Sync ─→ 状态 4: Rename

                                       崩溃点        安全
                                       ←─── ✗ ───→ 旧文件完好，新文件不可见
```

### 2.1 操作步骤

```go
// 1. 在同一目录创建临时文件
tmp, err := os.CreateTemp(dir, ".tmp-*")

// 2. 写入数据
tmp.Write(data)

// 3. 刷新到磁盘（确保数据不在 page cache 中丢失）
tmp.Sync()

// 4. 关闭文件描述符
tmp.Close()

// 5. 原子重命名：新文件瞬间替换旧文件（或创建新文件）
os.Rename(tmpName, destPath)
```

### 2.2 各步骤的作用

| 步骤 | 作用 | 省略会怎样 |
|------|------|-----------|
| `CreateTemp` | 在同一目录创建临时文件 | 确保在同一文件系统（跨设备无法 rename） |
| `Write` | 写入实际数据 | 写入失败时旧文件不受影响 |
| `Sync` | 调用 `fsync` 将数据从 page cache 刷入磁盘 | 防止操作系统崩溃时数据还在内存中 |
| `Close` | 关闭文件描述符 | 释放资源 |
| `Rename` | 原子替换 | Linux 上是原子操作，瞬间完成 |

## 3. 为什么 os.Rename 是原子的

### 3.1 POSIX 保证

POSIX 标准规定：`rename(oldpath, newpath)` 在同一文件系统上是原子操作。
要么完全成功，要么完全失败——不存在"成功了一半"的中间状态。

### 3.2 Linux 内核实现

在 Linux 上，`rename` 系统调用的原子性由内核保证：

1. 对于同一目录下的 rename：修改目录项是单个原子操作
2. 跨目录 rename：涉及两个目录项的修改，但 Linux VFS 层面使用 inode 锁保证原子性
3. **关键前提**：源和目标必须在**同一个文件系统**上。跨文件系统（如 ext4 → NFS）不保证原子性

### 3.3 崩溃场景分析

| 崩溃时刻 | 用户看到的文件状态 | 安全性 |
|---------|-----------------|--------|
| `CreateTemp` 之前 | 旧文件（或不存在） | 安全 |
| `CreateTemp` 之后，`Write` 之前 | 旧文件 + 不可见的临时文件 | 安全 |
| `Write` 中途 | 旧文件 + 不可见的临时文件 | 安全 |
| `Sync` 之前 | 旧文件 + 数据可能在内存中 | 旧文件仍安全 |
| `Sync` 之后，`Close` 之前 | 旧文件 + 临时文件（数据已落盘） | 安全 |
| `Close` 之后，`Rename` 之前 | 旧文件 + 完整的临时文件 | 安全 |
| `Rename` 执行中 | 原子操作，不会中断 | 安全 |
| `Rename` 之后 | 新文件 | 安全 |

**核心保证**：在 Rename 之前的任何时刻崩溃，用户看到的始终是旧的完整文件（或没有文件）。
Rename 完成后，用户看到的是新的完整文件。

## 4. 本项目的应用

### 4.1 数据文件写入

```go
func writeFile(destPath string, data []byte) error {
    dir := filepath.Dir(destPath)
    os.MkdirAll(dir, 0755)            // 自动创建嵌套目录

    tmp, _ := os.CreateTemp(dir, ".tmp-*")
    tmpName := tmp.Name()

    tmp.Write(data)                    // 写入数据
    tmp.Sync()                          // 刷到磁盘
    tmp.Close()                         // 关闭 fd
    return os.Rename(tmpName, destPath) // 原子替换
}
```

### 4.2 元数据文件写入

```go
func writeMeta(metaPath string, meta *ObjectMeta) error {
    dir := filepath.Dir(metaPath)
    tmp, _ := os.CreateTemp(dir, ".tmp-*.meta")

    data, _ := json.MarshalIndent(meta, "", "  ")
    tmp.Write(data)
    tmp.Sync()
    tmp.Close()

    return os.Rename(tmpName, metaPath)
}
```

### 4.3 PutObject 的写入顺序

```
PutObject
  ├─ 1. writeFile(dataPath, body)     ← 先写数据文件
  └─ 2. writeMeta(metaPath, meta)      ← 再写元数据文件
```

数据文件先于元数据写入。这意味着如果写入过程中崩溃：
- 元数据不存在 → 该对象不会被列举（ListObjectsV2 跳过无 .meta 的文件）
- 数据文件可能已写入但无元数据 → 存在孤儿文件，但不会影响服务正确性

### 4.4 失败时的清理

注意代码中的 `defer os.Remove(tmpName)`：

```go
tmp, err := os.CreateTemp(dir, ".tmp-*.meta")
tmpName := tmp.Name()
defer os.Remove(tmpName)  // ← 失败时清理临时文件
```

如果后续步骤（Write/Sync/Close/Rename）中任何一步失败，`defer` 确保临时文件被删除，
避免磁盘上残留 `.tmp-*` 文件。

## 5. 临时文件命名

`os.CreateTemp(dir, ".tmp-*")` 创建的文件名格式为：

```
/tmp-3245678901.meta    # 目录 + 随机后缀 + 指定后缀
```

- 同一目录下不会冲突（内核保证唯一性）
- `.tmp-` 前缀便于人工识别和批量清理
- 后缀（如 `.meta`）便于区分数据临时文件和元数据临时文件
