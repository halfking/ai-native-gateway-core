package admin

// extension_points.go —— **扩展点台账**的 admin 读端（P2-2）。
//
// # 这一页给运维看什么
//
// 一行一个扩展机制，**静态裁决**与**运行期计数**合并：
//
//	静态半（来自 `internal/extpoints` 的裁决表）：名称 / 代码位置 / 裁决 /
//	**是否在生产** / 所挂骨架 phase；
//	运行期半（来自 `internal/extpointledger`）：调用次数 / 失败数 / 超时数 /
//	丢弃数 / **最近窗口 p99** / 最近错误（文本 + 时间）。
//
// ★ 本文件是**薄壳**：合并逻辑与载荷形状住在 `internal/extpointledger`
// （⇒ 门可以住在 `scripts/ursmcheck`，★ CI **一定**跑；★ `admin` 包的测试套件
// 实测 65.8s，逼近 CI 快门的 120s 上限 ⇒ 门不能放这儿）。
//
// # 口径（响应里带 `ledger_scope`）
//
//	★ p99 是**最近窗口**的 p99（样本容量见每行的 `p99_window`），**不是**全量 p99
//	  ⇒ 因为台账是进程内的、可随时丢；
//	★ 台账**进程重启即清零** ⇒ 它是观测面，不是账本。

import (
	"net/http"

	"github.com/kaixuan/llm-gateway-go/internal/extpointledger"
)

// handleExtensionPoints 返回扩展点台账。
func (h *Handler) handleExtensionPoints(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, extpointledger.BuildPayload(extpointledger.Default()))
}
