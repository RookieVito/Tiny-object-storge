package handler

import (
	"log/slog"
	"net/http"
	"time"

	"tiny-object-storage/src/auth"
	"tiny-object-storage/src/config"
	"tiny-object-storage/src/cors"
	"tiny-object-storage/src/locks"
	"tiny-object-storage/src/metrics"
	"tiny-object-storage/src/storage"
)

// NewRouter 创建带有所有 S3 路由和中间件的 HTTP handler。
// clusterHandler 可选：非 nil 时注册 /_cluster/* 路由（分布式模式）。
// uiHandler 可选：非 nil 时注册 /_ui/* 路由（Web UI）。
func NewRouter(backend storage.StorageBackend, cfg *config.Config, m *metrics.Metrics, clusterHandler http.Handler, uiHandler http.Handler) http.Handler {
	bucketLocks := locks.NewBucketLocks()
	bm := NewBucketManager(backend, bucketLocks)
	om := NewObjectManager(backend, bucketLocks, cfg.MaxBodySize)
	mm := NewMultipartManager(backend, bucketLocks, cfg.MaxBodySize)
	a := auth.NewAuthenticatorWithRegion(cfg.AccessKey, cfg.SecretKey, cfg.Region)

	mux := http.NewServeMux()

	// Bucket 操作。
	mux.HandleFunc("GET /{$}", bm.ListBuckets)
	mux.HandleFunc("PUT /{bucket}", authWrap(a, "bucket", bm.CreateBucket))
	mux.HandleFunc("DELETE /{bucket}", authWrap(a, "bucket", bm.DeleteBucket))
	mux.HandleFunc("HEAD /{bucket}", authWrap(a, "bucket", bm.HeadBucket))
	// GET /{bucket} 同时处理 ListObjects 和 ListMultipartUploads（?uploads 参数）。
	mux.HandleFunc("GET /{bucket}", authWrap(a, "bucket", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("uploads") {
			mm.ListMultipartUploads(w, r)
		} else {
			bm.ListObjects(w, r)
		}
	}))

	// Object 操作。
	// PUT /{bucket}/{key...} 处理普通上传和 multipart UploadPart（?uploadId 参数）。
	mux.HandleFunc("PUT /{bucket}/{key...}", authWrap(a, "object", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("uploadId") {
			mm.UploadPart(w, r)
		} else {
			om.PutObject(w, r)
		}
	}))
	// GET /{bucket}/{key...} 处理普通下载和 multipart ListParts（?uploadId 参数）。
	mux.HandleFunc("GET /{bucket}/{key...}", authWrap(a, "object", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("uploadId") {
			mm.ListParts(w, r)
		} else {
			om.GetObject(w, r)
		}
	}))
	mux.HandleFunc("HEAD /{bucket}/{key...}", authWrap(a, "object", om.HeadObject))
	// DELETE /{bucket}/{key...} 处理普通删除和 multipart Abort（?uploadId 参数）。
	mux.HandleFunc("DELETE /{bucket}/{key...}", authWrap(a, "object", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("uploadId") {
			mm.AbortMultipartUpload(w, r)
		} else {
			om.DeleteObject(w, r)
		}
	}))
	// POST /{bucket}/{key...} 处理 multipart 操作：Initiate（?uploads）和 Complete（?uploadId）。
	mux.HandleFunc("POST /{bucket}/{key...}", authWrap(a, "object", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("uploads") {
			mm.InitiateMultipartUpload(w, r)
		} else if r.URL.Query().Has("uploadId") {
			mm.CompleteMultipartUpload(w, r)
		} else {
			http.Error(w, "InvalidRequest", http.StatusBadRequest)
		}
	}))

	// 用独立 mux 处理 metrics 端点，避免与 {bucket} 通配符冲突。
	topMux := http.NewServeMux()
	topMux.Handle("/_metrics", m)
	if clusterHandler != nil {
		topMux.Handle("/_cluster/", clusterHandler)
	}
	if uiHandler != nil {
		topMux.Handle("/_ui/", http.StripPrefix("/_ui", uiHandler))
	}
	topMux.Handle("/", mux)

	// 中间件链：s3Middleware → logMiddleware → corsMiddleware → topMux
	return s3Middleware(logMiddleware(m, cors.CORSMiddleware(cfg.CORS, topMux)))
}

// authWrap 创建验证 AWS Sig V2 的中间件，验证通过后调用下一个 handler。
// resourceType: "bucket" 或 "object"。空字符串跳过认证。
func authWrap(a *auth.Authenticator, resourceType string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if resourceType == "" {
			next.ServeHTTP(w, r)
			return
		}

		bucket := r.PathValue("bucket")
		key := ""
		if resourceType == "object" {
			key = r.PathValue("key")
		}

		if err := a.Authenticate(r, bucket, key); err != nil {
			http.Error(w, err.Error(), err.Status)
			return
		}

		next.ServeHTTP(w, r)
	}
}

// s3Middleware 添加公共响应头和 panic 恢复。
func s3Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>InternalError</Code><Message>internal server error</Message></Error>`))
			}
		}()

		w.Header().Set("Server", "tiny-object-storage/0.1")
		w.Header().Set("Date", time.Now().UTC().Format(time.RFC1123))

		next.ServeHTTP(w, r)
	})
}

// logMiddleware 记录结构化请求日志（method、path、status、latency），
// 并更新 metrics 计数器。
func logMiddleware(m *metrics.Metrics, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}

		next.ServeHTTP(sw, r)

		duration := time.Since(start)

		m.TotalRequests.Add(1)
		if sw.statusCode >= 400 {
			m.TotalErrors.Add(1)
		}

		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.statusCode,
			"duration_ms", duration.Milliseconds(),
			"remote_addr", r.RemoteAddr,
		)
	})
}

// statusWriter 包装 http.ResponseWriter 以捕获响应状态码。
type statusWriter struct {
	http.ResponseWriter
	statusCode int
}

func (sw *statusWriter) WriteHeader(code int) {
	sw.statusCode = code
	sw.ResponseWriter.WriteHeader(code)
}
