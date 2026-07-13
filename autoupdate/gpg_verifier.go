package autoupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// GPGVerifier GPG 签名验证器（P1 修复：下载完整性签名）
type GPGVerifier struct {
	publicKeyPath string
	gpgBinary     string
}

// NewGPGVerifier 创建 GPG 验证器
func NewGPGVerifier(publicKeyPath string) *GPGVerifier {
	gpgBinary := "gpg"
	if path, err := exec.LookPath("gpg2"); err == nil {
		gpgBinary = path
	} else if path, err := exec.LookPath("gpg"); err == nil {
		gpgBinary = path
	}

	return &GPGVerifier{
		publicKeyPath: publicKeyPath,
		gpgBinary:     gpgBinary,
	}
}

// VerifySignature 验证 GPG 签名
// filePath: 要验证的文件路径
// sigPath: .sig 或 .asc 签名文件路径
func (v *GPGVerifier) VerifySignature(ctx context.Context, filePath, sigPath string) error {
	if v.gpgBinary == "" {
		return fmt.Errorf("gpg binary not found in PATH")
	}

	// 导入公钥（如果尚未导入）
	if v.publicKeyPath != "" {
		importCmd := exec.CommandContext(ctx, v.gpgBinary, "--import", v.publicKeyPath)
		_ = importCmd.Run() // 忽略错误（公钥可能已导入）
	}

	// 验证签名
	verifyCmd := exec.CommandContext(ctx, v.gpgBinary, "--verify", sigPath, filePath)
	output, err := verifyCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("gpg verify failed: %w, output: %s", err, string(output))
	}

	return nil
}

// VerifyChecksumFile 验证 SHA256SUMS 文件（带 GPG 签名）
// checksumFilePath: SHA256SUMS 文件路径
// sigPath: SHA256SUMS.sig 签名文件路径
// targetFile: 要验证的目标文件路径
func (v *GPGVerifier) VerifyChecksumFile(ctx context.Context, checksumFilePath, sigPath, targetFile string) error {
	// 1. 验证 SHA256SUMS 文件的 GPG 签名
	if err := v.VerifySignature(ctx, checksumFilePath, sigPath); err != nil {
		return fmt.Errorf("checksum file signature invalid: %w", err)
	}

	// 2. 从 SHA256SUMS 文件中提取目标文件的期望 checksum
	expectedChecksum, err := extractChecksumFromFile(checksumFilePath, filepath.Base(targetFile))
	if err != nil {
		return fmt.Errorf("extract checksum: %w", err)
	}

	// 3. 计算目标文件的实际 checksum
	actualChecksum, err := calculateFileSHA256(targetFile)
	if err != nil {
		return fmt.Errorf("calculate file checksum: %w", err)
	}

	// 4. 比较
	if actualChecksum != expectedChecksum {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expectedChecksum, actualChecksum)
	}

	return nil
}

// extractChecksumFromFile 从 SHA256SUMS 文件中提取指定文件的 checksum
// 格式：<checksum>  <filename>
func extractChecksumFromFile(checksumFilePath, targetFilename string) (string, error) {
	data, err := os.ReadFile(checksumFilePath)
	if err != nil {
		return "", err
	}

	lines := string(data)
	// 简单解析：checksum + 两个空格 + filename
	for _, line := range splitLines(lines) {
		if len(line) < 66 { // SHA256 = 64 字符 + 至少 2 个空格
			continue
		}
		checksum := line[0:64]
		filename := line[66:]
		if filename == targetFilename {
			return checksum, nil
		}
	}

	return "", fmt.Errorf("checksum not found for %s", targetFilename)
}

func splitLines(s string) []string {
	var lines []string
	var line string
	for _, c := range s {
		if c == '\n' {
			if line != "" {
				lines = append(lines, line)
			}
			line = ""
		} else if c != '\r' {
			line += string(c)
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

func calculateFileSHA256(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}
