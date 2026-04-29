# Documentation Index

> 本文件是文档交叉引用索引。修改任何功能时，通过本文件快速定位需要同步更新的关联文档。
> 每篇文档顶部的 `<!-- tags: ... -->` 注释用于快速搜索：`grep -r "tags:.*关键词" docs/`

---

## 按主题分组

### 认证与安全
| 文档 | 主题 |
|------|------|
| [aws-sig-v2.md](technical/aws-sig-v2.md) | Sig V2 签名算法、StringToSign、验证流程 |
| [s3-protocol.md](technical/s3-protocol.md) | S3 错误码、API 概览 |
| [middleware-chain.md](technical/middleware-chain.md) | authWrap 中间件、认证在请求链中的位置 |
| [path-traversal.md](technical/path-traversal.md) | 路径遍历防御、安全层设计 |

### 存储与数据
| 文档 | 主题 |
|------|------|
| [storage-backend-interface.md](technical/storage-backend-interface.md) | StorageBackend 接口、策略模式、后端注册 |
| [atomic-writes.md](technical/atomic-writes.md) | 原子写入、crash recovery、tmpfile 重命名 |
| [metadata-sidecar.md](technical/metadata-sidecar.md) | .meta 侧边文件格式、ObjectMeta 结构 |
| [erasure-coding.md](technical/erasure-coding.md) | GF(2^8)、Reed-Solomon 编解码、ECBackend |
| [multipart-upload.md](technical/multipart-upload.md) | 分片上传协议、MultipartStorage 接口 |
| [versioning.md](technical/versioning.md) | 对象版本控制、VersionedBackend 装饰器、Delete Marker |

### 分布式系统
| 文档 | 主题 |
|------|------|
| [consistent-hashing.md](technical/consistent-hashing.md) | Ketama 一致性哈希、虚拟节点 |
| [gossip-protocol.md](technical/gossip-protocol.md) | SWIM 简化版、成员管理、Incarnation |
| [quorum-read-write.md](technical/quorum-read-write.md) | Quorum R/W、N/R/W 一致性保证 |

### HTTP 路由与中间件
| 文档 | 主题 |
|------|------|
| [go-serve-mux.md](technical/go-serve-mux.md) | Go 1.22+ 路由模式、路径参数 |
| [middleware-chain.md](technical/middleware-chain.md) | 请求处理管道、s3→log→auth→handler |
| [list-objects-v2.md](technical/list-objects-v2.md) | ListObjectsV2 算法、分页、前缀过滤 |

### 运维与可观测性
| 文档 | 主题 |
|------|------|
| [graceful-shutdown.md](technical/graceful-shutdown.md) | 信号处理、连接排空 |
| [metrics.md](technical/metrics.md) | 原子计数器、`/_metrics` 端点 |
| [per-bucket-locks.md](technical/per-bucket-locks.md) | 并发安全、per-bucket mutex |

### 架构与使用
| 文档 | 主题 |
|------|------|
| [architecture.md](architecture.md) | 系统架构、组件图、路由表、磁盘布局 |
| [usage.md](usage.md) | Web UI、CLI 客户端、curl 示例、配置参考、测试指南 |

---

## 交叉引用矩阵

修改某功能时，需要同步更新的文档：

| 当你修改... | 需要同步检查... |
|-------------|----------------|
| **认证逻辑**（auth/） | `aws-sig-v2.md`, `s3-protocol.md`, `middleware-chain.md`, `architecture.md`§5, `usage.md`§3 |
| **Presigned URL**（auth/presign） | `architecture.md`§5, `usage.md`§3, `s3-protocol.md` |
| **路由/端点**（handler/router） | `go-serve-mux.md`, `middleware-chain.md`, `s3-protocol.md`, `architecture.md`§4 |
| **存储接口**（storage/） | `storage-backend-interface.md`, `architecture.md`§Package structure |
| **元数据格式**（service/） | `metadata-sidecar.md`, `architecture.md`§Disk layout |
| **EC 编解码**（ec/） | `erasure-coding.md`, `architecture.md`§Disk layout(EC) |
| **分布式**（hash/, cluster/） | `consistent-hashing.md`, `gossip-protocol.md`, `quorum-read-write.md` |
| **Multipart** | `multipart-upload.md`, `s3-protocol.md`, `architecture.md`§Disk layout(multipart) |
| **版本控制**（storage/versioning） | `versioning.md`, `storage-backend-interface.md`, `architecture.md`§Disk layout, `metadata-sidecar.md` |
| **CLI 客户端**（cmd/client/） | `usage.md`§2, `CLAUDE.md`§CLI Client |
| **Web UI**（web/） | `usage.md`§1, `CLAUDE.md`§Web UI |
| **Config**（config/） | `usage.md`§5, `architecture.md`§Key types |
| **CORS**（cors/） | `middleware-chain.md`, `usage.md`§5, `architecture.md`§9 |
| **Metrics** | `metrics.md`, `middleware-chain.md` |
| **错误码**（s3error/） | `s3-protocol.md`, `usage.md`§4 |
| **新增 Phase** | `TODO.md`, `CLAUDE.md`§Testing, `architecture.md`§Phase history, 本文件 |

---

## 标签索引

用 `grep -rl "tags:.*<tag>" docs/` 快速查找相关文档。

| 标签 | 文档 |
|------|------|
| `authentication` | aws-sig-v2.md, middleware-chain.md, s3-protocol.md, index.md |
| `presign` | index.md, architecture.md, usage.md |
| `concurrency` | per-bucket-locks.md |
| `distributed` | consistent-hashing.md, gossip-protocol.md, quorum-read-write.md |
| `error-handling` | s3-protocol.md |
| `http-routing` | go-serve-mux.md, middleware-chain.md |
| `cors` | middleware-chain.md, index.md, architecture.md, usage.md |
| `multipart` | multipart-upload.md, storage-backend-interface.md |
| `versioning` | versioning.md, storage-backend-interface.md, architecture.md |
| `security` | aws-sig-v2.md, path-traversal.md |
| `storage` | storage-backend-interface.md, atomic-writes.md, metadata-sidecar.md, erasure-coding.md |
| `observability` | metrics.md, middleware-chain.md |
| `api` | s3-protocol.md, go-serve-mux.md, list-objects-v2.md |
