# Tiny Object Storage

一个兼容 S3 协议的轻量对象存储服务，纯 Go 标准库实现，零外部依赖。

## Quick Start

```bash
# 编译
go build -o tiny-storage ./cmd/server/

# 启动（默认端口 9000，凭证 minioadmin/minioadmin）
./tiny-storage

# Web UI
# 浏览器打开 http://localhost:9000/_ui/

# CLI 客户端
go run ./cmd/client/ config --endpoint http://localhost:9000 --access-key minioadmin --secret-key minioadmin
go run ./cmd/client/ mb my-bucket
go run ./cmd/client/ cp README.md s3://my-bucket/README.md
go run ./cmd/client/ ls my-bucket
```

## 功能概览

- S3 兼容 API（AWS Signature V2 认证）
- 三种存储后端：Local / Erasure Coding / Distributed
- Web UI 管理控制台
- Go CLI 客户端
- 结构化 JSON 日志 + Metrics 端点

## 文档

| 文档 | 说明 |
|------|------|
| [使用指南](docs/usage.md) | CLI 客户端、Web UI、curl API、AWS CLI、SDK 等所有访问方式 |
| [架构设计](docs/architecture.md) | 系统架构、请求流程、存储后端设计 |
| [CLAUDE.md](CLAUDE.md) | 开发指南（构建、测试、编码规范） |
