package cors

import (
	"fmt"
	"net/http"
	"strings"

	"tiny-object-storage/src/config"
)

// CORSMiddleware 返回处理 CORS 的 HTTP 中间件。
func CORSMiddleware(cfg config.CORSConfig, next http.Handler) http.Handler {
	if !cfg.Enabled || len(cfg.AllowedOrigins) == 0 {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}

		if !matchOrigin(cfg.AllowedOrigins, origin) {
			next.ServeHTTP(w, r)
			return
		}

		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Origin", allowOriginValue(cfg, origin))
			w.Header().Set("Access-Control-Allow-Methods", strings.Join(cfg.AllowedMethods, ", "))
			w.Header().Set("Access-Control-Allow-Headers", strings.Join(cfg.AllowedHeaders, ", "))
			w.Header().Set("Access-Control-Max-Age", fmtMaxAge(cfg.MaxAge))
			if cfg.AllowCredentials {
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}

		w.Header().Set("Access-Control-Allow-Origin", allowOriginValue(cfg, origin))
		if cfg.AllowCredentials {
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		}
		if len(cfg.ExposeHeaders) > 0 {
			w.Header().Set("Access-Control-Expose-Headers", strings.Join(cfg.ExposeHeaders, ", "))
		}

		next.ServeHTTP(w, r)
	})
}

// matchOrigin 检查 origin 是否在允许列表中。
func matchOrigin(allowed []string, origin string) bool {
	for _, a := range allowed {
		if a == "*" {
			return true
		}
		if a == origin {
			return true
		}
	}
	return false
}

// allowOriginValue 返回 Access-Control-Allow-Origin 的值。
// 通配符 "*" 时不返回具体 origin（浏览器行为）。
func allowOriginValue(cfg config.CORSConfig, origin string) string {
	if matchOrigin(cfg.AllowedOrigins, "*") {
		if !cfg.AllowCredentials {
			return "*"
		}
		return origin
	}
	return origin
}

// fmtMaxAge 返回 MaxAge 的字符串表示。
func fmtMaxAge(maxAge int) string {
	return fmt.Sprintf("%d", maxAge)
}
