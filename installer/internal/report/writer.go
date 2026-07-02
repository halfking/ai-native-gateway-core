// Package report 生成部署报告（install-report.md）
package report

import (
	"fmt"
	"os"
	"text/template"
	"time"

	"github.com/kaixuan/llm-gateway-go/installer/internal/dockerutil"
	"github.com/kaixuan/llm-gateway-go/installer/internal/envdetect"
	"github.com/kaixuan/llm-gateway-go/installer/internal/prompt"
)

// InstallReportData 报告数据
type InstallReportData struct {
	InstallerVersion string
	InstallTime      string
	OSInfo           *envdetect.OSInfo
	Config           *prompt.InstallConfig
	AppImageTag      string
	ImageSources     ImageSourceSummary
	Health           *dockerutil.HealthReport
}

// ImageSourceSummary 镜像来源汇总
type ImageSourceSummary struct {
	App   string
	Citus string
	Redis string
}

// Write 生成 install-report.md
func Write(path string, data *InstallReportData, tplContent string) error {
	tpl, err := template.New("report").Parse(tplContent)
	if err != nil {
		return fmt.Errorf("解析报告模板失败: %w", err)
	}

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("创建报告文件失败: %w", err)
	}
	defer f.Close()

	if err := tpl.Execute(f, data); err != nil {
		return fmt.Errorf("渲染报告失败: %w", err)
	}
	return nil
}

// Now 获取当前时间字符串
func Now() string {
	return time.Now().Format("2006-01-02 15:04:05")
}
