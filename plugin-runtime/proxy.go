package pluginruntime

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// PluginStaticHandler serves static files from <pluginsDir>/<pluginId>/web/.
// Path values: pluginId, rest (rest is the path under web/; empty or trailing "/"
// serves index.html). Rejects path traversal.
//
// Files are streamed with http.ServeContent (rather than http.ServeFile) so the
// handler controls status codes and avoids ServeFile's built-in index.html and
// directory redirects, which would otherwise turn 404s and clean index serves
// into 301 responses.
func PluginStaticHandler(pluginsDir string) http.Handler {
	root := filepath.Clean(pluginsDir)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pluginID := r.PathValue("pluginId")
		rest := r.PathValue("rest")
		// Reject pluginId that could escape the root.
		if pluginID == "" || strings.ContainsAny(pluginID, "/\\") || pluginID == ".." {
			http.Error(w, "invalid plugin id", http.StatusBadRequest)
			return
		}
		if rest == "" || strings.HasSuffix(rest, "/") {
			rest = rest + "index.html"
		}
		webDir := filepath.Join(root, pluginID, "web")
		target := filepath.Clean(filepath.Join(webDir, rest))
		// Ensure target stays inside webDir.
		if !strings.HasPrefix(target, webDir+string(filepath.Separator)) && target != webDir {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}
		f, err := os.Open(target)
		if err != nil {
			// Plugin pages such as aggregations are Vue routes, not files.
			// Serve the SPA shell for extension-less paths so the host nav
			// can open them; missing assets still 404.
			if os.IsNotExist(err) && !strings.Contains(filepath.Base(rest), ".") {
				index := filepath.Join(webDir, "index.html")
				if idx, ierr := os.Open(index); ierr == nil {
					defer idx.Close()
					if st, serr := idx.Stat(); serr == nil && !st.IsDir() {
						http.ServeContent(w, r, "index.html", st.ModTime(), idx)
						return
					}
				}
			}
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		stat, err := f.Stat()
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if stat.IsDir() {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, filepath.Base(target), stat.ModTime(), f)
	})
}
