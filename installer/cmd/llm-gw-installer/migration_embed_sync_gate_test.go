//go:build !integration

// Package main — migration_embed_sync_gate_test.go
//
// migration_embed_sync_gate_test.go —— installer embed 与 canonical 启动迁移
// 的**通用**双点同步门。
//
// 为什么要有这道门（AGENTS.md「已完成」第 6 条记录的复发族）：
// 每条新迁移都必须在 6 个点位同步登记——canonical SQL、installer
// go:embed 指令、installer 启动文件表、dbinit runner、部署通道脚本
// files 数组、installed_startup_migrations.tsv。2026-10-11 审计实测过
// 两轮半成品：851 只同步了一半（e47c1102b），849/850 完全没同步；更早
// 还有 845-851 整段通道腿漏登（存量八连红）。这些缺口的共同形态是
// **「canonical 改了／新增了，embed 副本没跟上」**——CI 构建出的安装器
// 于是带着与正典不同的 SQL 上门，而单条迁移的判据照常全绿。
//
// 既有判据只覆盖 627-630/635 五条（migration_627_630_contract_test.go
// 的显式 cases 列表），新迁移不在列表里就等于没有字节门。本文件把它扩到
// 全量：凡 embeddata 里存在的文件，一律要求与 canonical 逐字节相同。
//
// 门口径为什么不是「所有 canonical 迁移都必须 embed」：embeddata 是
// 精选子集（660 条 canonical up 迁移里嵌 359 条，00x-3xx 一整段与
// 7xx/8xx 的 30 条历史件按设计不嵌）。所以正典→embed 方向只能对
// 「≥849 的新增段」设门（2026-10-11 之后的全部，见下），embed→canonical
// 方向则对交集设全量字节门。

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const (
	embedStartupDir     = "embeddata/startup"
	canonicalStartupDir = "../../../sql/migrations/startup"

	// minMandatoryEmbedMigration 是「必须出现在 embeddata 里」的起始序号。
	// 取 849：AGENTS.md 记载 849/850/851 是第一轮被查出的漏同步三连，
	// 从此往后的每一条新迁移都必须五点同步（本门负责其中最容易漏的
	// embed 副本这一维）。
	minMandatoryEmbedMigration = 849
)

// TestInstallerEmbedStartupMigrationsByteMatchCanonical 是通用字节门：
// embeddata/startup 下每个文件，若 canonical 有同名件，两者必须逐字节相同。
func TestInstallerEmbedStartupMigrationsByteMatchCanonical(t *testing.T) {
	entries, err := os.ReadDir(embedStartupDir)
	if err != nil {
		t.Fatalf("read %s: %v", embedStartupDir, err)
	}

	var compared int
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		canonicalPath := filepath.Join(canonicalStartupDir, e.Name())
		canonical, err := os.ReadFile(canonicalPath)
		if os.IsNotExist(err) {
			// embed 里有、canonical 没有：installers 可能自带仅交付用的
			// 脚本，不在本门范围（反方向漏同步由下一条测试覆盖）。
			continue
		}
		if err != nil {
			t.Errorf("read canonical %s: %v", e.Name(), err)
			continue
		}
		embed, err := os.ReadFile(filepath.Join(embedStartupDir, e.Name()))
		if err != nil {
			t.Errorf("read embed %s: %v", e.Name(), err)
			continue
		}
		compared++
		if string(embed) != string(canonical) {
			t.Errorf("embed 漂移 %s：installer/cmd/llm-gw-installer/%s 与 %s 逐字节不一致；"+
				"改了一处忘了另一处，会让 CI 安装器带着非正典 SQL 上门（849/850/851 那一族）",
				e.Name(), embedStartupDir, canonicalPath)
		}
	}

	if compared < 100 {
		t.Fatalf("字节门只比对了 %d 个文件（预期数百）—— 量具本身可能失效（embeddata 路径漂移？）", compared)
	}
	t.Logf("字节门比对 %d 个 embed/canonical 同名件", compared)
}

// TestCanonicalStartupMigrationsFrom849AreEmbedded 堵住 849/850/851 那一族：
// 849 及之后的每条 canonical up 迁移都必须有 embed 副本。
func TestCanonicalStartupMigrationsFrom849AreEmbedded(t *testing.T) {
	entries, err := os.ReadDir(canonicalStartupDir)
	if err != nil {
		t.Fatalf("read %s: %v", canonicalStartupDir, err)
	}

	embedEntries, err := os.ReadDir(embedStartupDir)
	if err != nil {
		t.Fatalf("read %s: %v", embedStartupDir, err)
	}
	embedded := make(map[string]bool, len(embedEntries))
	for _, e := range embedEntries {
		embedded[e.Name()] = true
	}

	var checked int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") ||
			strings.HasSuffix(e.Name(), ".down.sql") {
			continue
		}
		num, ok := migrationNumber(e.Name())
		if !ok || num < minMandatoryEmbedMigration {
			continue
		}
		checked++
		if !embedded[e.Name()] {
			t.Errorf("迁移 %s 未登记进 installer embeddata：新迁移必须五点同步，"+
				"漏了这一点的历史故障是 849/850/851 与 845-851 通道腿",
				e.Name())
		}
	}

	if checked == 0 {
		t.Fatalf("没有任何 ≥%d 的迁移被检查到 —— 门口径失效（文件名解析或目录漂移？）",
			minMandatoryEmbedMigration)
	}
	t.Logf("强制 embed 门检查 %d 条 ≥%d 的 canonical 迁移", checked, minMandatoryEmbedMigration)
}

func migrationNumber(name string) (int, bool) {
	idx := strings.Index(name, "_")
	if idx <= 0 {
		return 0, false
	}
	n, err := strconv.Atoi(name[:idx])
	if err != nil {
		return 0, false
	}
	return n, true
}
