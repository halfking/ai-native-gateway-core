package admin

import (
	dbpkg "github.com/kaixuan/llm-gateway-go/db"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// sessionBodiesNativeReadSetting 是 bodies 读端灰度开关。
//
// 默认 **false**：走 `request_logs_bodies_with_current_month`（v1）。
// 理由是数据而不是「没写完」——生产 252 实测 2026-09-30 一天仍有 1,113 条
// turn 有 v1 正文而没有 `session_bodies` 行（审计 §9.229.2）。
// 打开会让那一天的会话导出正文变成 `{}`，而接口仍返回 200。
const sessionBodiesNativeReadSetting = "storage.admin_session_bodies_native_read"

// sessionBodiesFromSQL returns the aliased bodies FROM-source for
// session-scoped readers (export / compare).
//
// 两个形状的列名一致（都是 `request_body` / `response_body`），
// 所以调用点的 SELECT 投影**不需要**跟着改——这正是
// `db.SessionFamilyBodiesSourceSQL` 存在的理由：两侧列名不同
// （`request_delta` / `response_delta`），映射必须收在这一层，
// 否则每个调用点都得自己写一遍 `AS request_body`。
//
// 形状与 `logsSourceFromSQL()` 同款（同一套灰度约定），便于对照。
func sessionBodiesFromSQL() string {
	if settings.GetPlatformBool(sessionBodiesNativeReadSetting, false) {
		return dbpkg.SessionFamilyBodiesSourceSQL() + " rb"
	}
	return "request_logs_bodies_with_current_month rb"
}
