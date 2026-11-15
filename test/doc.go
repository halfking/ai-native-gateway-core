// Package test 承载注入检测（F-41）的独立验证套件。
//
// 2026-11-14 注入检测验证轮的原交付形态是两份 `package main` 验证程序
// （各自带 main()）——同目录双 main 冲突 + 其中一份 import 了不存在的
// `domains` 根包（正确为 `domain`），`go build ./...` 恒红。本文件把该
// 目录立为普通包，两份验证程序改编为 `_test.go`（由 `go test ./test/`
// 运行，行为口径不变）。
package test
