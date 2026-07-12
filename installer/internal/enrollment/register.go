package enrollment

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// RegisterRequest 实例注册请求
type RegisterRequest struct {
	InstanceID     string `json:"instance_id"`
	InstanceType   string `json:"instance_type"` // "standalone" / "k8s-deployment" / "docker"
	DeploymentID   string `json:"deployment_id"` // 仅 K8s
	ReplicaCount   int    `json:"replica_count"` // 仅 K8s
	Hostname       string `json:"hostname"`
	IPAddress      string `json:"ip_address"`
	Version        string `json:"version"`
	LicenseKeyHash string `json:"license_key_hash"`
	HardwareHash   string `json:"hardware_hash"`
	PublicKey      string `json:"public_key"` // Ed25519 base64
}

// RegisterResponse 实例注册响应
type RegisterResponse struct {
	InstanceToken   string    `json:"instance_token"`
	RefreshToken    string    `json:"refresh_token"`
	ServerPublicKey string    `json:"server_public_key"`
	ExpiresAt       time.Time `json:"expires_at"`
}

// Client 注册客户端
type Client struct {
	BaseURL    string
	HTTPClient *http.Client
}

// NewClient 创建注册客户端
func NewClient(baseURL string) *Client {
	if baseURL == "" {
		baseURL = "https://llm.kxpms.cn"
	}
	return &Client{
		BaseURL: baseURL,
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Register 向主控端注册实例
func (c *Client) Register(ctx context.Context, req RegisterRequest) (*RegisterResponse, error) {
	// 验证必填字段
	if req.InstanceID == "" {
		return nil, fmt.Errorf("instance_id is required")
	}
	if req.LicenseKeyHash == "" {
		return nil, fmt.Errorf("license_key_hash is required")
	}
	if req.HardwareHash == "" {
		return nil, fmt.Errorf("hardware_hash is required")
	}
	if req.PublicKey == "" {
		return nil, fmt.Errorf("public_key is required")
	}

	// K8s 模式验证
	if req.InstanceType == "k8s-deployment" {
		if req.DeploymentID == "" {
			return nil, fmt.Errorf("deployment_id is required for k8s-deployment")
		}
		if req.ReplicaCount < 1 {
			req.ReplicaCount = 1
		}
	}

	// 构造请求
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := c.BaseURL + "/api/v1/instances/register"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	httpReq.Body = io.NopCloser(jsonReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "llm-gw-installer/1.0")

	// 发送请求
	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	// 处理错误状态码
	if resp.StatusCode >= 400 {
		// 409 表示设备数超限
		if resp.StatusCode == http.StatusConflict {
			var errResp map[string]string
			if json.Unmarshal(respBody, &errResp) == nil {
				if errResp["error"] == "device_limit_exceeded" {
					return nil, fmt.Errorf("device limit exceeded (status 409)")
				}
			}
		}
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	// 解析响应
	var registerResp RegisterResponse
	if err := json.Unmarshal(respBody, &registerResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return &registerResp, nil
}

// jsonReader 返回一个 io.Reader
func jsonReader(data []byte) io.Reader {
	return &jsonReaderImpl{data: data, pos: 0}
}

type jsonReaderImpl struct {
	data []byte
	pos  int
}

func (r *jsonReaderImpl) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}
