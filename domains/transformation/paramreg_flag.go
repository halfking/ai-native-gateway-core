package transformation

import (
	"os"
	"strings"
	"sync"
)

// paramregEnabled 报告是否启用参数注册表驱动的扩展字段还原。
//
// PARAMREG_ENABLED=false 退回旧的双门禁行为（同协议 + 同 catalog 才还原），
// 用于紧急回退。默认启用。
//
// 与 internal/ir 里的同名函数读同一个环境变量，保证两层行为一致 ——
// 只回退一层会造成"IR 层还原了但 transport 层又丢弃"的割裂状态。
var paramregEnabled = sync.OnceValue(func() bool {
	return !strings.EqualFold(strings.TrimSpace(os.Getenv("PARAMREG_ENABLED")), "false")
})
