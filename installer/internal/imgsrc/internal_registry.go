package imgsrc

import (
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// RegistryAuth registry 凭证
type RegistryAuth struct {
	Username string
	Password string
}

// InternalRegistrySource 内部 registry 源
type InternalRegistrySource struct {
	Registry string  // e.g. "registry.kxpms.cn"
	Project  string  // e.g. "kaixuan"
	Auth     *RegistryAuth
	Insecure bool    // 是否允许 HTTP（默认 HTTPS）
}

// NewInternalRegistrySource 创建内部 registry 源
// project: registry 下的项目路径，如 "kaixuan"。为空时使用镜像原名（不加前缀）。
func NewInternalRegistrySource(registry string, auth *RegistryAuth) *InternalRegistrySource {
	return &InternalRegistrySource{
		Registry: registry,
		Project:  "kaixuan", // 默认项目
		Auth:     auth,
		Insecure: false,
	}
}

// NewInternalRegistrySourceWithProject 自定义项目路径
func NewInternalRegistrySourceWithProject(registry, project string, auth *RegistryAuth) *InternalRegistrySource {
	return &InternalRegistrySource{
		Registry: registry,
		Project:  project,
		Auth:     auth,
		Insecure: false,
	}
}

// Name 返回源名
func (s *InternalRegistrySource) Name() string {
	return fmt.Sprintf("internal-registry:%s", s.Registry)
}

// Available 探测 registry 是否可达
func (s *InternalRegistrySource) Available() (bool, error) {
	scheme := "https"
	if s.Insecure {
		scheme = "http"
	}
	url := fmt.Sprintf("%s://%s/v2/", scheme, s.Registry)

	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest("HEAD", url, nil)
	if err != nil {
		return false, err
	}
	if s.Auth != nil {
		req.SetBasicAuth(s.Auth.Username, s.Auth.Password)
	}

	resp, err := client.Do(req)
	if err != nil {
		return false, nil  // 网络错误视为不可用
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 {
		// 401 表明 registry 存在但需要认证
		return true, fmt.Errorf("需要认证")
	}
	return resp.StatusCode < 500, nil
}

// Pull 拉取镜像（自动加上内部 registry 前缀）
// 原始 image: "kx-llm-gateway-go:v1.0.0" → "registry.kxpms.cn/kaixuan/kx-llm-gateway-go:v1.0.0"
// 原始 image: "citusdata/citus:11.3.0" → "registry.kxpms.cn/kaixuan/citusdata/citus:11.3.0"
func (s *InternalRegistrySource) Pull(image string) error {
	fullRef := s.fullReference(image)

	// 设置 docker login（如有凭证）
	if s.Auth != nil {
		if err := s.dockerLogin(); err != nil {
			return fmt.Errorf("docker login 失败: %w", err)
		}
	}

	// docker pull
	if err := dockerPull(fullRef); err != nil {
		return err
	}

	// 重打 tag 到原名（如需要）
	return dockerTagIfNeeded(fullRef, image)
}

// fullReference 返回完整 registry 引用
func (s *InternalRegistrySource) fullReference(image string) string {
	// 如果 image 已包含 registry 前缀，原样返回
	for _, prefix := range []string{s.Registry + "/", "docker.io/"} {
		if len(image) > len(prefix) && image[:len(prefix)] == prefix {
			return image
		}
	}
	return fmt.Sprintf("%s/%s/%s", s.Registry, s.Project, image)
}

// dockerLogin 登录 registry（用 --password-stdin 避免密码出现在进程列表）
func (s *InternalRegistrySource) dockerLogin() error {
	args := []string{"login", s.Registry, "-u", s.Auth.Username, "--password-stdin"}
	cmd := exec.Command("docker", args...)
	// 通过 stdin 传密码（不进 ps/进程列表）
	cmd.Stdin = strings.NewReader(s.Auth.Password + "\n")
	// 丢弃输出避免凭证泄露到日志（错误也吞掉，调用方看 exit code）
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}
