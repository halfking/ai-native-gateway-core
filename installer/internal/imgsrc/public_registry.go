package imgsrc

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// PublicRegistrySource 公网 registry 源（含国内 mirror）
type PublicRegistrySource struct {
	Registry string  // "docker.io" 或 "registry.cn-hangzhou.aliyuncs.com"
}

// NewPublicRegistrySource 创建公网 registry 源
func NewPublicRegistrySource(registry string) *PublicRegistrySource {
	return &PublicRegistrySource{Registry: registry}
}

// Name 返回源名
func (s *PublicRegistrySource) Name() string {
	switch s.Registry {
	case "docker.io":
		return "docker-hub"
	case "registry.cn-hangzhou.aliyuncs.com":
		return "aliyun-mirror"
	default:
		return s.Registry
	}
}

// Available 探测 registry 是否可达
func (s *PublicRegistrySource) Available() (bool, error) {
	url := s.v2Endpoint()
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest("HEAD", url, nil)
	if err != nil {
		return false, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, nil
	}
	defer resp.Body.Close()
	return resp.StatusCode < 500, nil
}

// Pull 拉取镜像
// docker.io: 直接 pull 原 image
// 国内 mirror: 加前缀（citusdata/citus:11.3.0 → registry.cn-hangzhou.aliyuncs.com/citusdata/citus:11.3.0）
func (s *PublicRegistrySource) Pull(image string) error {
	fullRef := s.fullReference(image)

	if err := dockerPull(fullRef); err != nil {
		return err
	}

	// 重打 tag 到原 image 名（mirror 模式下需要）
	if fullRef != image {
		if err := dockerTagIfNeeded(fullRef, image); err != nil {
			return fmt.Errorf("重打 tag 失败: %w", err)
		}
	}
	return nil
}

// fullReference 返回完整引用
func (s *PublicRegistrySource) fullReference(image string) string {
	if s.Registry == "docker.io" {
		// docker.io 是默认 registry，image 原样可用
		return image
	}

	// 国内 mirror: 在 image 前面加 mirror 前缀
	// citusdata/citus:11.3.0 → registry.cn-hangzhou.aliyuncs.com/citusdata/citus:11.3.0
	// redis:7-alpine → registry.cn-hangzhou.aliyuncs.com/library/redis:7-alpine
	parts := strings.Split(image, "/")
	if len(parts) == 1 {
		// 单段名称（如 redis:7-alpine），加 library/
		return fmt.Sprintf("%s/library/%s", s.Registry, image)
	}

	// 检查是否已经包含 registry 前缀
	if strings.HasPrefix(parts[0], "registry.") || strings.Contains(parts[0], ".") {
		return image
	}

	return fmt.Sprintf("%s/%s", s.Registry, image)
}

// v2Endpoint 返回 v2 API endpoint
func (s *PublicRegistrySource) v2Endpoint() string {
	if s.Registry == "docker.io" {
		return "https://registry-1.docker.io/v2/"
	}
	return fmt.Sprintf("https://%s/v2/", s.Registry)
}
