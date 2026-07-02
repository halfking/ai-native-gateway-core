package imgsrc

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// OfflineTarSource 离线 tarball 源（最高优先级）
type OfflineTarSource struct {
	TarDir string
}

// NewOfflineTarSource 创建离线 tar 源
func NewOfflineTarSource(tarDir string) *OfflineTarSource {
	return &OfflineTarSource{TarDir: tarDir}
}

// Name 返回源名
func (s *OfflineTarSource) Name() string {
	return "offline-tarball"
}

// Available 检查 tar 目录是否存在且含 tar 文件
func (s *OfflineTarSource) Available() (bool, error) {
	if _, err := os.Stat(s.TarDir); os.IsNotExist(err) {
		return false, fmt.Errorf("目录不存在: %s", s.TarDir)
	}

	matches, err := filepath.Glob(filepath.Join(s.TarDir, "*.tar.gz"))
	if err != nil {
		return false, err
	}
	return len(matches) > 0, nil
}

// Pull 通过 docker load 从 tar 加载
func (s *OfflineTarSource) Pull(image string) error {
	// 解析 image 找到对应 tar
	tarFile := s.findTarball(image)
	if tarFile == "" {
		return fmt.Errorf("离线包未含镜像 %s", image)
	}

	cmd := exec.Command("docker", "load", "-i", tarFile)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker load %s: %w\n%s", tarFile, err, string(out))
	}

	// 验证 docker tag 是否正确
	if !s.imageLoaded(image) {
		return fmt.Errorf("docker load 成功但未找到 tag %s", image)
	}
	return nil
}

// findTarball 根据 image 找到对应 tar 文件
// 支持的命名约定：
//   kx-llm-gateway-go:v1.0.0 → images/kx-llm-gateway-go-v1.0.0.tar.gz
//   citusdata/citus:11.3.0    → images/kx-citus-v11.3.0.tar.gz (kx- 前缀是默认)
//   redis:7-alpine            → images/kx-redis-v7-alpine.tar.gz
func (s *OfflineTarSource) findTarball(image string) string {
	parts := strings.SplitN(image, ":", 2)
	if len(parts) != 2 {
		return ""
	}
	name := parts[0]
	tag := parts[1]

	// 取 name 的最后一段（去掉 namespace）
	nameParts := strings.Split(name, "/")
	baseName := nameParts[len(nameParts)-1]

	// 尝试多种命名
	candidates := []string{
		filepath.Join(s.TarDir, fmt.Sprintf("%s-%s.tar.gz", baseName, tag)),
		filepath.Join(s.TarDir, fmt.Sprintf("kx-%s-%s.tar.gz", baseName, tag)),
	}

	// 如果是 name 包含 namespace（如 citusdata/citus），尝试 "kx-{baseName}-{tag}"
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}

	// 最后尝试通配符匹配
	matches, _ := filepath.Glob(filepath.Join(s.TarDir, fmt.Sprintf("*%s-%s.tar.gz", baseName, tag)))
	if len(matches) > 0 {
		return matches[0]
	}

	return ""
}

// imageLoaded 检查 docker 是否已加载指定 tag
func (s *OfflineTarSource) imageLoaded(image string) bool {
	cmd := exec.Command("docker", "image", "inspect", image)
	return cmd.Run() == nil
}
