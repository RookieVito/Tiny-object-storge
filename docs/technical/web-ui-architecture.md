<!-- tags: web-ui, spa, embed, sig-v4, react -->
# Web UI 架构

## 概述

Web UI 是一个独立的 React 单页应用（SPA），通过 `go:embed` 嵌入到 Go 服务器二进制中。浏览器端通过 Web Crypto API 实现 AWS Sig V4 签名，直接调用 S3 API——无需后端代理层。

## 1. 整体架构

```
┌──────────────────────────────────────────────┐
│  Go 服务器（单一二进制）                        │
│  ┌────────────────┐  ┌────────────────────┐  │
│  │  S3 API Router │  │  go:embed UI 静态   │  │
│  │  (/_ui/{path}) │  │  cmd/server/       │  │
│  │                │  │  embed.go          │  │
│  └────────────────┘  └────────────────────┘  │
└──────────────────────────┬───────────────────┘
                           │ HTTP
┌──────────────────────────▼───────────────────┐
│  浏览器                                       │
│  ┌────────────────────────────────────────┐  │
│  │  React SPA                              │  │
│  │  ┌──────────┐  ┌───────────────────┐   │  │
│  │  │ Sig V4   │  │ S3 API Client     │   │  │
│  │  │ Signer   │→ │ (XML 解析)        │   │  │
│  │  └──────────┘  └───────────────────┘   │  │
│  │  ┌──────────┐  ┌───────────────────┐   │  │
│  │  │LoginScreen│ │  ObjectBrowser    │   │  │
│  │  │BucketList│  │  Breadcrumb       │   │  │
│  │  └──────────┘  └───────────────────┘   │  │
│  └────────────────────────────────────────┘  │
└──────────────────────────────────────────────┘
```

## 2. Go:embed 静态资源嵌入

```go
// cmd/server/embed.go
//go:embed static/dist/*
var uiFS embed.FS

type spaHandler struct {
    fs http.FileSystem
}

func (h spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    // 尝试直接提供文件
    f, err := h.fs.Open(strings.TrimPrefix(path, "/"))
    if err == nil {
        stat, _ := f.Stat()
        if stat != nil && !stat.IsDir() {
            http.FileServer(h.fs).ServeHTTP(w, r)
            return
        }
    }
    // 回退到 index.html（SPA 路由）
    r.URL.Path = "/"
    http.FileServer(h.fs).ServeHTTP(w, r)
}
```

**SPA 回退机制**：`/ui/buckets`、`/ui/objects/mybucket` 等客户端路由路径在服务端不存在对应文件，回退到 `index.html` 让 React Router 处理。

**编译时检查**：`var _ fs.FS = uiFS` 确保 `embed.FS` 在编译期实现 `fs.FS` 接口。

**构建流程**：
```bash
cd web && npm run build     # 输出到 web/dist/
# 手动复制到 cmd/server/static/dist/（或 CI 自动化）
go build ./cmd/server/      # go:embed 编译嵌入
```

## 3. 客户端 Sig V4 签名

```typescript
// web/src/api/signer.ts
export async function signV4(
    method: string,
    contentType: string,
    canonicalResource: string,
    secretKey: string,
    region: string,
    accessKey: string,
): Promise<V4Result>
```

**实现要点**：
- 使用 Web Crypto API 的 `crypto.subtle.importKey` + `crypto.subtle.sign` 进行 HMAC-SHA256
- 完整实现 Sig V4 规范：构造 Canonical Request → String to Sign → Signing Key → Signature
- 日期格式：`YYYYMMDDTHHmmssZ`
- Signing Key 派生链：`SecretKey → DateKey → DateRegionKey → DateRegionServiceKey → SigningKey`

**为什么不用后端代理**：浏览器端签名意味着后端只需实现 S3 API，Web UI 和 CLI 客户端使用完全相同的认证路径，减少攻击面和复杂度。

## 4. S3 API 客户端

```typescript
// web/src/api/client.ts
export async function s3Request(
    config: S3Config,
    method: string,
    path: string,
    options?: { body?: BodyInit; contentType?: string },
): Promise<{ ok: boolean; status: number; body: string; headers: Headers }>
```

**关键功能**：
- 自动签名：每次请求前调用 `signV4` 生成 Authorization 头
- XML 解析：ListBuckets、ListObjects 等响应使用 DOMParser 解析 XML
- 文件上传：支持带进度回调的 `uploadObject`，通过 `XMLHttpRequest`（非 fetch）获取 `upload.onprogress`

## 5. 组件结构

```
web/src/
├── api/
│   ├── client.ts        # S3 HTTP 客户端（签名 + 请求封装）
│   ├── signer.ts        # Sig V4 签名实现
│   ├── types.ts         # TypeScript 类型定义
│   └── xml-parser.ts    # S3 XML 响应解析
├── components/
│   ├── App.tsx           # 根组件（路由状态管理）
│   ├── LoginScreen.tsx   # 登录界面（输入 endpoint/accessKey/secretKey）
│   ├── BucketList.tsx    # Bucket 列表（创建、删除、进入）
│   ├── ObjectBrowser.tsx # 对象浏览器（列表、上传、下载、删除）
│   └── Breadcrumb.tsx    # 面包屑导航
├── hooks/
│   └── useAuth.tsx       # 认证上下文（localStorage 持久化）
└── main.tsx              # 入口
```

### 认证流程

```typescript
// useAuth.tsx
// 凭证存储在 localStorage，页面刷新后自动恢复
const [config, setConfig] = useState<S3Config | null>(() => {
    const saved = localStorage.getItem('tos_config')
    return saved ? JSON.parse(saved) : null
})
```

### 文件上传

```typescript
// ObjectBrowser.tsx + client.ts
// 使用 XMLHttpRequest 获取上传进度
export function uploadObject(config, path, file, onProgress) {
    const xhr = new XMLHttpRequest()
    xhr.upload.onprogress = (e) => {
        onProgress(e.loaded, e.total)
    }
    // 签名后 PUT 请求
}
```

## 6. 开发与生产模式

| 模式 | 访问方式 | 说明 |
|------|---------|------|
| 开发 | `http://localhost:5173` | Vite dev server，API 请求 proxy 到 `:9000` |
| 生产 | `http://localhost:9000/_ui/` | Go 服务器直接提供嵌入的静态文件 |

## 对应实现

| 文件 | 说明 |
|------|------|
| `cmd/server/embed.go` | go:embed 声明 + SPA fallback handler |
| `web/src/api/signer.ts` | 浏览器端 Sig V4 签名 |
| `web/src/api/client.ts` | S3 HTTP 客户端 |
| `web/src/api/xml-parser.ts` | XML 响应解析 |
| `web/src/components/ObjectBrowser.tsx` | 对象管理主界面 |
| `web/src/hooks/useAuth.tsx` | 认证上下文 |
