// Package imgsrc 提供 4 层镜像源兜底链：
// [1] 离线包内 docker load
// [2] 内部 registry (registry.kxpms.cn)
// [3] 国内 mirror (registry.cn-hangzhou.aliyuncs.com)
// [4] 官方源 (docker.io)
package imgsrc

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// ImageSource 镜像源接口
type ImageSource interface {
	Name() string
	Available() (bool, error)  // 此源是否可用（探测）
	Pull(image string) error    // 拉取镜像
}

// ImageSpec 镜像规格
type ImageSpec struct {
	Name string  // "kx-llm-gateway-go"
	Tag  string  // "v1.0.0"
}

// FullReference 返回完整引用，如 "kx-llm-gateway-go:v1.0.0"
func (s ImageSpec) FullReference() string {
	return fmt.Sprintf("%s:%s", s.Name, s.Tag)
}

// PullStrategy 拉取策略（按优先级串联多个源）
type PullStrategy struct {
	Sources []ImageSource
}

// NewDefaultStrategy 默认策略（4 层 fallback）
func NewDefaultStrategy(installDir, internalRegistry string, auth *RegistryAuth) *PullStrategy {
	return &PullStrategy{
		Sources: []ImageSource{
			NewOfflineTarSource(filepath.Join(installDir, "images")),
			NewInternalRegistrySource(internalRegistry, auth),
			NewPublicRegistrySource("registry.cn-hangzhou.aliyuncs.com"), // 国内 mirror
			NewPublicRegistrySource("docker.io"),                         // 官方
		},
	}
}

// Pull 智能 fallback 拉取
func (s *PullStrategy) Pull(image ImageSpec, logger func(string)) error {
	imageRef := image.FullReference()
	var errs []string

	for _, src := range s.Sources {
		ok, probeErr := src.Available()
		if !ok {
			msg := fmt.Sprintf("⊘ %s 不可用", src.Name())
			if probeErr != nil {
				msg += fmt.Sprintf(" (%v)", probeErr)
			}
			logger("  " + msg)
			continue
		}

		logger(fmt.Sprintf("▶ 尝试 %s: %s ...", src.Name(), imageRef))
		if err := src.Pull(imageRef); err != nil {
			logger(fmt.Sprintf("  ✗ %s 失败: %v", src.Name(), err))
			errs = append(errs, fmt.Sprintf("%s: %v", src.Name(), err))
			continue
		}

		logger(fmt.Sprintf("✅ %s 拉取成功 (来源: %s)", imageRef, src.Name()))
		return nil
	}

	return fmt.Errorf("所有镜像源都失败:\n  - %s", strings.Join(errs, "\n  - "))
}

// dockerTagIfNeeded 重打 tag（如镜像来自 mirror 但需要原名）
func dockerTagIfNeeded(src, dst string) error {
	if src == dst {
		return nil
	}
	cmd := exec.Command("docker", "tag", src, dst)
	return cmd.Run()
}

// dockerPull 直接 docker pull（其他 source 内部使用）
func dockerPull(ref string) error {
	cmd := exec.Command("docker", "pull", ref)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker pull %s: %w\n%s", ref, err, string(out))
	}
	return nil
}
