package cors

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"tiny-object-storage/src/config"
)

// CORSMiddleware 返回处理 CORS 的 HTTP 中间件。
func CORSMiddleware(cfg config.CORSConfig, next http.Handler) http.Handler {
	if len(cfg.AllowedOrigins) == 0 {
		return next
	}

	if cfg.AllowCredentials && hasWildcard(cfg.AllowedOrigins) {
		slog.Warn("CORS: AllowCredentials=true with wildcard origin is insecure; specify explicit origins")
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}

		matched := matchOrigin(cfg.AllowedOrigins, origin)
		if !matched {
			w.Header().Add("Vary", "Origin")
			next.ServeHTTP(w, r)
			return
		}

		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Origin", allowOriginValue(cfg, origin, matched))
			w.Header().Set("Access-Control-Allow-Methods", strings.Join(cfg.AllowedMethods, ", "))
			w.Header().Set("Access-Control-Allow-Headers", strings.Join(cfg.AllowedHeaders, ", "))
			w.Header().Set("Access-Control-Max-Age", strconv.Itoa(cfg.MaxAge))
			if cfg.AllowCredentials {
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}

		w.Header().Set("Access-Control-Allow-Origin", allowOriginValue(cfg, origin, matched))
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

// hasWildcard 检查允许列表是否包含通配符。
func hasWildcard(allowed []string) bool {
	for _, a := range allowed {
		if a == "*" {
			return true
		}
	}
	return false
}

// allowOriginValue 返回 Access-Control-Allow-Origin 的值。
// 通配符 "*" 时不返回具体 origin（浏览器行为），除非 AllowCredentials。
func allowOriginValue(cfg config.CORSConfig, origin string, matched bool) string {
	if !matched {
		return ""
	}
	if hasWildcard(cfg.AllowedOrigins) {
		if !cfg.AllowCredentials {
			return "*"
		}
		return origin
	}
	return origin
}
