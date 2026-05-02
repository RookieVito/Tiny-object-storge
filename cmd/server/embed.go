package main

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed static/dist/*
var uiFS embed.FS

// spaHandler 为 SPA 提供静态文件服务，未匹配的路径回退到 index.html。
type spaHandler struct {
	fs http.FileSystem
}

func (h spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// 尝试直接提供文件。
	f, err := h.fs.Open(strings.TrimPrefix(path, "/"))
	if err == nil {
		stat, _ := f.Stat()
		if stat != nil && !stat.IsDir() {
			f.Close()
			http.FileServer(h.fs).ServeHTTP(w, r)
			return
		}
		f.Close()
	}

	// 回退到 index.html（SPA 路由）。
	r.URL.Path = "/"
	http.FileServer(h.fs).ServeHTTP(w, r)
}

var _ fs.FS = uiFS // 编译时检查 uiFS 实现 fs.FS。
