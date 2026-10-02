package admin

// request_logs_v1_direct_tables.go — R32（2026-10-02，P1-2 根修）：把
// v1DirectTables / canonicalView 从 !integration 标签文件里抽到无标签共享
// 文件。它们是**纯 Go 字面量表**（v1 宽族关系名与 canonical 视图名），无任何
// 真库依赖；此前住在 v1_direct_padded_column_reader_test.go（//go:build
// !integration）里，而 §9.49 新增的 request_logs_indirect_readers_test.go
// （无标签）与 request_logs_control_plane_dependency_test.go（无标签）引用
// 了它们 → -tags=integration 树编译红（undefined），连带
// sql/schema TestIntegrationTaggedTreeCompiles 红——integration 真库门整棵
// 树不可编译。字面量进共享文件后两棵树同源，不再有标签缝隙。
//
// 语义与登记责任不变：改动名单仍由 v1_direct_padded_column_reader_test.go
// 的 TestNoUnregisteredVPaddedColumnReader 与 indirect_readers 的 ResolvesTo
// 核对双侧钉住。

// v1DirectTables 是「绕过视图直读」判定里的 v1 宽族关系名。
var v1DirectTables = map[string]bool{
	"request_logs": true, "request_logs_hot": true,
	"request_logs_bodies": true, "request_logs_bodies_hot": true,
}

const canonicalView = "request_logs_with_current_month"
