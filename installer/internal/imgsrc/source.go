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
	return s.PullWithAlias(image, "", logger)
}

// PullWithAlias 拉取镜像，并在成功后把它重打 tag 为 alias（若 alias 非空且不同于原始引用）。
// 用于：从公网拉 citusdata/citus:11.3.0 后，重打 tag 成 compose 引用的 kx-citus:v11.3.0。
func (s *PullStrategy) PullWithAlias(image ImageSpec, alias string, logger func(string)) error {
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

		// 可选：重打 alias tag
		if alias != "" && alias != imageRef {
			if err := EnsureAlias(imageRef, alias); err != nil {
				logger(fmt.Sprintf("  ⚠️  重打 alias tag %s → %s 失败: %v", imageRef, alias, err))
			} else {
				logger(fmt.Sprintf("  ✅ alias: %s → %s", imageRef, alias))
			}
		}
		return nil
	}

	return fmt.Errorf("所有镜像源都失败:\n  - %s", strings.Join(errs, "\n  - "))
}

// EnsureAlias 确保本地存在 alias 引用（若 src 已存在但 dst 不存在，则 docker tag）
// 用于把上游原始镜像名（citusdata/citus:11.3.0）映射成 compose 引用名（kx-citus:v11.3.0）
func EnsureAlias(src, alias string) error {
	if src == alias {
		return nil
	}
	// dst 已存在则跳过
	if exec.Command("docker", "image", "inspect", alias).Run() == nil {
		return nil
	}
	// src 不存在则无法 tag
	if exec.Command("docker", "image", "inspect", src).Run() != nil {
		return fmt.Errorf("源镜像 %s 不存在", src)
	}
	out, err := exec.Command("docker", "tag", src, alias).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker tag %s %s: %w\n%s", src, alias, err, string(out))
	}
	return nil
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
