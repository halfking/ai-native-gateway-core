package autoupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
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

	// 创建临时文件
	filename := filepath.Base(url)
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
