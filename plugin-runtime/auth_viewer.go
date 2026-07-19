package pluginruntime

import "net/http"

// AuthInfo 是从 Gateway 鉴权上下文提取的最小角色信息。
type AuthInfo struct {
	Super        bool
	PlatformOps  bool
	TenantPortal bool
}

// AuthExtractor 从请求中解析出 AuthInfo。真正的实现由 cmd/gateway 在启动时注入
// （桥接 admin.AuthContext）。当请求没有鉴权上下文时返回 ok=false
// （调用方应视为未认证 / 零值 viewer）。
type AuthExtractor func(*http.Request) (AuthInfo, bool)

// defaultExtractor 在 cmd/gateway 注入真实实现之前返回 ok=false。
var defaultExtractor AuthExtractor = func(*http.Request) (AuthInfo, bool) {
	return AuthInfo{}, false
}

// SetAuthExtractor 注入真实的 extractor。在 gateway 启动时调用一次。
func SetAuthExtractor(ex AuthExtractor) {
	if ex != nil {
		defaultExtractor = ex
	}
}

// ViewerFromRequest 通过注入的 extractor 从请求的鉴权上下文中派生出 ViewerOpts。
// 未认证时返回零值 ViewerOpts。
func ViewerFromRequest(r *http.Request) ViewerOpts {
	return viewerWith(r, defaultExtractor)
}

// viewerWith 使用显式 extractor 派生 ViewerOpts（便于测试，
// 避免修改包级别的默认值）。
func viewerWith(r *http.Request, ex AuthExtractor) ViewerOpts {
	if ex == nil {
		ex = defaultExtractor
	}
	info, ok := ex(r)
	if !ok {
		return ViewerOpts{}
	}
	return ViewerOpts{
		IsSuper:        info.Super,
		IsPlatformOps:  info.PlatformOps,
		IsTenantPortal: info.TenantPortal,
	}
}
