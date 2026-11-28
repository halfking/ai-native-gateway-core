package autoupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"time"
)

// Downloader 下载器（HTTP/OSS + 校验 + 断点续传）
type Downloader struct {
	httpClient  *http.Client
	downloadDir string
	gpgVerifier *GPGVerifier // P1: GPG 签名验证器（可选）
}

// NewDownloader 创建下载器
func NewDownloader(downloadDir string) *Downloader {
	return &Downloader{
		httpClient: &http.Client{
			Timeout: 10 * time.Minute,
		},
		downloadDir: downloadDir,
	}
}

// DownloadResult 下载结果
type DownloadResult struct {
	FilePath   string
	Checksum   string
	Size       int64
	DurationMs int64
}

// Download 下载文件（支持断点续传）
func (d *Downloader) Download(ctx context.Context, url, expectedChecksum string) (*DownloadResult, error) {
	start := time.Now()

	// 解析 URL：path 段定文件名（查询串不参与），url 本身原样请求。
	u, err := neturl.Parse(url)
	if err != nil {
		return nil, fmt.Errorf("parse download url: %w", err)
	}
	// 创建临时文件。★ 文件名只取 URL 的 path 段：maintain 的下载地址带
	// ?ticket=<HMAC> 查询串（10 分钟票），license-authority 的旧静态地址没有。
	// 旧实现用整个 URL 的 Base() 当文件名 ⇒ 查询串进文件名 ⇒ JWT 一来就是
	// 「file name too long」，对着 maintain 出票的地址**永远装不上**。
	filename := filepath.Base(u.Path)
	if filename == "" || filename == "/" || filename == "." {
		filename = "llm-gateway-go"
	}
	tmpPath := filepath.Join(d.downloadDir, filename+".tmp")
	finalPath := filepath.Join(d.downloadDir, filename)

	// 检查是否已存在并验证
	if info, err := os.Stat(finalPath); err == nil {
		if checksum, err := d.calculateChecksum(finalPath); err == nil && checksum == expectedChecksum {
			return &DownloadResult{
				FilePath:   finalPath,
				Checksum:   checksum,
				Size:       info.Size(),
				DurationMs: time.Since(start).Milliseconds(),
			}, nil
		}
	}

	// 确保下载目录存在
	if err := os.MkdirAll(d.downloadDir, 0755); err != nil {
		return nil, fmt.Errorf("create download dir: %w", err)
	}

	// 创建请求
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	// 支持断点续传
	if info, err := os.Stat(tmpPath); err == nil {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", info.Size()))
	}

	// 执行下载
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return nil, fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}

	// 打开文件（续传或新建）
	var out *os.File
	if resp.StatusCode == http.StatusPartialContent {
		out, err = os.OpenFile(tmpPath, os.O_APPEND|os.O_WRONLY, 0644)
	} else {
		out, err = os.Create(tmpPath)
	}
	if err != nil {
		return nil, fmt.Errorf("open temp file: %w", err)
	}
	defer func() { _ = out.Close() }()

	// 写入数据
	if _, err := io.Copy(out, resp.Body); err != nil {
		return nil, fmt.Errorf("write data: %w", err)
	}

	// ★ 制品是**可执行文件**：下载器必须自己把它置成可执行。
	//   旧实现用 0644 落盘 ⇒ 安装器在替换之前做校验时看到的是「不可执行」
	//   ⇒ 要么结构校验失败，要么 `<bin> --version` 报 permission denied
	//   ⇒ 安装 100% 失败。这条与「--version 契约」是同型死结。
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return nil, fmt.Errorf("chmod downloaded artifact: %w", err)
	}

	// 验证校验和
	checksum, err := d.calculateChecksum(tmpPath)
	if err != nil {
		return nil, fmt.Errorf("calculate checksum: %w", err)
	}

	if checksum != expectedChecksum {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("checksum mismatch: expected %s, got %s", expectedChecksum, checksum)
	}

	// 移动到最终位置
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return nil, fmt.Errorf("move to final path: %w", err)
	}

	info, _ := os.Stat(finalPath)
	return &DownloadResult{
		FilePath:   finalPath,
		Checksum:   checksum,
		Size:       info.Size(),
		DurationMs: time.Since(start).Milliseconds(),
	}, nil
}

// calculateChecksum 计算文件 SHA256
func (d *Downloader) calculateChecksum(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// Cleanup 清理下载目录
func (d *Downloader) Cleanup(olderThan time.Duration) error {
	entries, err := os.ReadDir(d.downloadDir)
	if err != nil {
		return err
	}

	cutoff := time.Now().Add(-olderThan)
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}

		if info.ModTime().Before(cutoff) {
			path := filepath.Join(d.downloadDir, entry.Name())
			_ = os.Remove(path)
		}
	}

	return nil
}

// SetGPGVerifier 配置 GPG 验证器（P1：可选配置）
func (d *Downloader) SetGPGVerifier(verifier *GPGVerifier) {
	d.gpgVerifier = verifier
}

// DownloadWithGPG 下载并验证 GPG 签名（P1 修复：完整性签名）
// url: 文件下载 URL
// sigURL: .sig 签名文件 URL
// expectedChecksum: 期望的 SHA256（可选，如果提供则额外验证）
func (d *Downloader) DownloadWithGPG(ctx context.Context, url, sigURL, expectedChecksum string) (*DownloadResult, error) {
	if d.gpgVerifier == nil {
		return nil, fmt.Errorf("GPG verifier not configured")
	}

	// 1. 下载文件
	result, err := d.Download(ctx, url, expectedChecksum)
	if err != nil {
		return nil, err
	}

	// 2. 下载签名文件
	sigFilePath := result.FilePath + ".sig"
	sigReq, err := http.NewRequestWithContext(ctx, "GET", sigURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create sig request: %w", err)
	}

	sigResp, err := d.httpClient.Do(sigReq)
	if err != nil {
		return nil, fmt.Errorf("download signature: %w", err)
	}
	defer sigResp.Body.Close()

	if sigResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("signature download failed: status %d", sigResp.StatusCode)
	}

	sigFile, err := os.Create(sigFilePath)
	if err != nil {
		return nil, fmt.Errorf("create sig file: %w", err)
	}
	defer sigFile.Close()

	if _, err := io.Copy(sigFile, sigResp.Body); err != nil {
		return nil, fmt.Errorf("write sig file: %w", err)
	}
	sigFile.Close()

	// 3. 验证 GPG 签名
	if err := d.gpgVerifier.VerifySignature(ctx, result.FilePath, sigFilePath); err != nil {
		return nil, fmt.Errorf("GPG signature verification failed: %w", err)
	}

	return result, nil
}
