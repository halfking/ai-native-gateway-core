package streaming

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// StaticHandler serves the Vue SPA from the web/dist directory.
// It falls back to index.html for SPA client-side routing.
type StaticHandler struct {
	distDir string
	fs      http.Handler
}

func NewStaticHandler(distDir string) *StaticHandler {
	if distDir == "" {
		return nil
	}
	info, err := os.Stat(distDir)
	if err != nil || !info.IsDir() {
		return nil
	}
	return &StaticHandler{
		distDir: distDir,
		fs:      http.FileServer(http.Dir(distDir)),
	}
}

func (h *StaticHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	upath := r.URL.Path
	if !strings.HasPrefix(upath, "/") {
		upath = "/" + upath
	}

	fpath := filepath.Join(h.distDir, filepath.Clean(upath))
	if info, err := os.Stat(fpath); err == nil && !info.IsDir() {
		// NET-010 fix: 静态文件扩展名白名单。
		//
		// 修复前：web/dist 下任何文件都直接对外暴露（若 .env / config.json.bak
		// / 备份 SQL 等被误打包会泄露）。
		// 修复后：仅允许明确的静态资源扩展名（.html / .js / .css / .svg / .png
		// / .ico / .woff2 等）。.json 故意不放白名单 —— 配置应在 build 期打包
		// 进 JS bundle；单独保留 .json 是配置泄露的常见途径。
		ext := strings.ToLower(filepath.Ext(fpath))
		if !isAllowedStaticExt(ext) {
			http.NotFound(w, r)
			return
		}
		h.fs.ServeHTTP(w, r)
		return
	}

	if strings.HasPrefix(upath, "/v1/") || strings.HasPrefix(upath, "/api/") || strings.HasPrefix(upath, "/healthz") {
		http.NotFound(w, r)
		return
	}

	indexFile := filepath.Join(h.distDir, "index.html")
	if _, err := os.Stat(indexFile); err == nil {
		http.ServeFile(w, r, indexFile)
		return
	}

	http.NotFound(w, r)
}

// allowedStaticExt 白名单（NET-010 fix）。任何不在列表里的扩展名一律 404。
var allowedStaticExt = map[string]bool{
	".html":  true,
	".js":    true,
	".mjs":   true,
	".css":   true,
	".map":   true,
	".svg":   true,
	".png":   true,
	".jpg":   true,
	".jpeg":  true,
	".gif":   true,
	".webp":  true,
	".ico":   true,
	".woff":  true,
	".woff2": true,
	".ttf":   true,
	".eot":   true,
	".otf":   true,
	".mp4":   true,
	".webm":  true,
	".mp3":   true,
	".wasm":  true,
	".txt":   true, // robots.txt / sitemap.txt
}

func isAllowedStaticExt(ext string) bool {
	return allowedStaticExt[ext]
}
