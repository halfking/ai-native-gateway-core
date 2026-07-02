// Command storage-migrate 附件存储迁移工具
//
// 用于在不同存储后端之间迁移附件数据：
//   - 本地 → OSS
//   - 本地 → S3
//   - OSS → S3
//   - 本地 → 本地（目录迁移）
//
// 使用方式：
//
//	./storage-migrate \
//	  --source-type=local --source-dir=/data/attachments \
//	  --target-type=oss --target-oss-endpoint=oss-cn-hangzhou.aliyuncs.com \
//	  --target-oss-bucket=my-bucket --target-oss-ak=xxx --target-oss-sk=xxx \
//	  --workers=10 --dry-run
//
// 功能：
//   - 遍历源存储的所有附件文件
//   - 并发上传到目标存储（可配置并发数）
//   - 显示进度条和统计信息
//   - 支持断点续传（跳过已存在的文件）
//   - 支持 dry-run 模式（仅统计，不实际迁移）
//   - 生成迁移报告（成功/失败/跳过的文件列表）
//
package main

import (
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/attachments"
)

// 命令行参数
var (
	// 源存储配置
	sourceType   = flag.String("source-type", "local", "源存储类型: local, oss, s3")
	sourceDir    = flag.String("source-dir", "", "源存储目录（local 类型必填）")
	sourceOSSEp  = flag.String("source-oss-endpoint", "", "源 OSS endpoint")
	sourceOSSBkt = flag.String("source-oss-bucket", "", "源 OSS bucket")
	sourceOSSAK  = flag.String("source-oss-ak", "", "源 OSS AccessKey")
	sourceOSSSK  = flag.String("source-oss-sk", "", "源 OSS SecretKey")
	sourceS3Ep   = flag.String("source-s3-endpoint", "", "源 S3 endpoint")
	sourceS3Rgn  = flag.String("source-s3-region", "us-east-1", "源 S3 region")
	sourceS3Bkt  = flag.String("source-s3-bucket", "", "源 S3 bucket")
	sourceS3AK   = flag.String("source-s3-ak", "", "源 S3 AccessKey")
	sourceS3SAK  = flag.String("source-s3-sak", "", "源 S3 SecretAccessKey")

	// 目标存储配置
	targetType   = flag.String("target-type", "oss", "目标存储类型: local, oss, s3")
	targetDir    = flag.String("target-dir", "", "目标存储目录（local 类型必填）")
	targetOSSEp  = flag.String("target-oss-endpoint", "", "目标 OSS endpoint")
	targetOSSBkt = flag.String("target-oss-bucket", "", "目标 OSS bucket")
	targetOSSAK  = flag.String("target-oss-ak", "", "目标 OSS AccessKey")
	targetOSSSK  = flag.String("target-oss-sk", "", "目标 OSS SecretKey")
	targetS3Ep   = flag.String("target-s3-endpoint", "", "目标 S3 endpoint")
	targetS3Rgn  = flag.String("target-s3-region", "us-east-1", "目标 S3 region")
	targetS3Bkt  = flag.String("target-s3-bucket", "", "目标 S3 bucket")
	targetS3AK   = flag.String("target-s3-ak", "", "目标 S3 AccessKey")
	targetS3SAK  = flag.String("target-s3-sak", "", "目标 S3 SecretAccessKey")

	// 迁移选项
	workers    = flag.Int("workers", 10, "并发上传数")
	dryRun     = flag.Bool("dry-run", false, "仅统计，不实际迁移")
	skipExists = flag.Bool("skip-exists", true, "跳过目标已存在的文件")
	reportFile = flag.String("report", "migration-report.txt", "迁移报告文件")
)

// 统计信息
type stats struct {
	total   int64
	success int64
	skipped int64
	failed  int64
	bytes   int64
}

func main() {
	flag.Parse()

	if err := run(); err != nil {
		log.Fatalf("迁移失败: %v", err)
	}
}

func run() error {
	log.Printf("存储迁移工具启动")
	log.Printf("源存储: %s", *sourceType)
	log.Printf("目标存储: %s", *targetType)
	if *dryRun {
		log.Printf("*** DRY RUN 模式 - 仅统计不迁移 ***")
	}

	// 初始化源和目标后端
	sourceBackend, err := createBackend(*sourceType, sourceConfig())
	if err != nil {
		return fmt.Errorf("创建源后端失败: %w", err)
	}

	targetBackend, err := createBackend(*targetType, targetConfig())
	if err != nil {
		return fmt.Errorf("创建目标后端失败: %w", err)
	}

	// 健康检查
	log.Printf("检查源存储连接...")
	if err := sourceBackend.HealthCheck(); err != nil {
		return fmt.Errorf("源存储不可用: %w", err)
	}
	log.Printf("✓ 源存储连接正常")

	log.Printf("检查目标存储连接...")
	if err := targetBackend.HealthCheck(); err != nil {
		return fmt.Errorf("目标存储不可用: %w", err)
	}
	log.Printf("✓ 目标存储连接正常")

	// 扫描源存储的文件列表
	log.Printf("扫描源存储文件...")
	files, err := listFiles(sourceBackend, *sourceType, *sourceDir)
	if err != nil {
		return fmt.Errorf("扫描文件失败: %w", err)
	}
	log.Printf("✓ 找到 %d 个文件", len(files))

	if len(files) == 0 {
		log.Printf("没有文件需要迁移")
		return nil
	}

	// 执行迁移
	st := &stats{}
	st.total = int64(len(files))

	startTime := time.Now()
	if err := migrate(sourceBackend, targetBackend, files, st); err != nil {
		return err
	}
	elapsed := time.Since(startTime)

	// 打印统计信息
	log.Printf("\n========== 迁移完成 ==========")
	log.Printf("总文件数: %d", st.total)
	log.Printf("成功: %d", st.success)
	log.Printf("跳过: %d", st.skipped)
	log.Printf("失败: %d", st.failed)
	log.Printf("传输字节: %d (%.2f MB)", st.bytes, float64(st.bytes)/1024/1024)
	log.Printf("耗时: %v", elapsed)
	log.Printf("平均速度: %.2f MB/s", float64(st.bytes)/1024/1024/elapsed.Seconds())

	return nil
}

// createBackend 根据配置创建存储后端
func createBackend(typ string, cfg map[string]string) (attachments.StorageBackend, error) {
	switch strings.ToLower(typ) {
	case "local":
		dir := cfg["dir"]
		if dir == "" {
			return nil, fmt.Errorf("local 类型必须指定目录")
		}
		return attachments.NewLocalStorageBackend(dir)

	case "oss":
		return attachments.NewOSSStorageBackend(attachments.OSSConfig{
			Endpoint:        cfg["endpoint"],
			AccessKeyID:     cfg["ak"],
			AccessKeySecret: cfg["sk"],
			BucketName:      cfg["bucket"],
			BasePath:        cfg["base_path"],
		})

	case "s3":
		return attachments.NewS3StorageBackend(attachments.S3Config{
			Endpoint:        cfg["endpoint"],
			Region:          cfg["region"],
			AccessKeyID:     cfg["ak"],
			SecretAccessKey: cfg["sak"],
			BucketName:      cfg["bucket"],
			BasePath:        cfg["base_path"],
			UsePathStyle:    cfg["endpoint"] != "",
			UseSSL:          cfg["endpoint"] == "" || strings.HasPrefix(cfg["endpoint"], "https"),
		})

	default:
		return nil, fmt.Errorf("不支持的存储类型: %s", typ)
	}
}

// sourceConfig 返回源存储配置
func sourceConfig() map[string]string {
	switch strings.ToLower(*sourceType) {
	case "local":
		return map[string]string{"dir": *sourceDir}
	case "oss":
		return map[string]string{
			"endpoint": *sourceOSSEp,
			"bucket":   *sourceOSSBkt,
			"ak":       *sourceOSSAK,
			"sk":       *sourceOSSSK,
		}
	case "s3":
		return map[string]string{
			"endpoint": *sourceS3Ep,
			"region":   *sourceS3Rgn,
			"bucket":   *sourceS3Bkt,
			"ak":       *sourceS3AK,
			"sak":      *sourceS3SAK,
		}
	}
	return nil
}

// targetConfig 返回目标存储配置
func targetConfig() map[string]string {
	switch strings.ToLower(*targetType) {
	case "local":
		return map[string]string{"dir": *targetDir}
	case "oss":
		return map[string]string{
			"endpoint": *targetOSSEp,
			"bucket":   *targetOSSBkt,
			"ak":       *targetOSSAK,
			"sk":       *targetOSSSK,
		}
	case "s3":
		return map[string]string{
			"endpoint": *targetS3Ep,
			"region":   *targetS3Rgn,
			"bucket":   *targetS3Bkt,
			"ak":       *targetS3AK,
			"sak":      *targetS3SAK,
		}
	}
	return nil
}

// listFiles 列举源存储的所有文件
func listFiles(backend attachments.StorageBackend, typ, dir string) ([]string, error) {
	var files []string

	// 对于本地存储，直接遍历目录
	if strings.ToLower(typ) == "local" {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // 忽略错误
			}
			if d.IsDir() {
				return nil
			}
			// 转换为相对路径
			relPath, _ := filepath.Rel(dir, path)
			files = append(files, filepath.ToSlash(relPath))
			return nil
		})
		return files, err
	}

	// 对于云存储，暂不支持列举（需要 SDK 支持）
	return nil, fmt.Errorf("云存储列举功能尚未实现，请先使用本地作为源")
}

// migrate 执行迁移
func migrate(src, dst attachments.StorageBackend, files []string, st *stats) error {
	// 打开报告文件
	report, err := os.Create(*reportFile)
	if err != nil {
		return fmt.Errorf("创建报告文件失败: %w", err)
	}
	defer report.Close()

	// 任务队列
	tasks := make(chan string, len(files))
	for _, f := range files {
		tasks <- f
	}
	close(tasks)

	// 启动 worker
	var wg sync.WaitGroup
	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for relPath := range tasks {
				if err := migrateFile(src, dst, relPath, st, report); err != nil {
					log.Printf("[Worker %d] 迁移失败 %s: %v", workerID, relPath, err)
				}
				// 打印进度
				done := atomic.LoadInt64(&st.success) + atomic.LoadInt64(&st.skipped) + atomic.LoadInt64(&st.failed)
				if done%100 == 0 || done == st.total {
					log.Printf("进度: %d/%d (%.1f%%)", done, st.total, float64(done)/float64(st.total)*100)
				}
			}
		}(i)
	}

	wg.Wait()
	return nil
}

// migrateFile 迁移单个文件
func migrateFile(src, dst attachments.StorageBackend, relPath string, st *stats, report *os.File) error {
	// 检查目标是否已存在
	if *skipExists {
		exists, err := dst.FileExists(relPath)
		if err == nil && exists {
			atomic.AddInt64(&st.skipped, 1)
			fmt.Fprintf(report, "SKIP\t%s\n", relPath)
			return nil
		}
	}

	if *dryRun {
		atomic.AddInt64(&st.success, 1)
		fmt.Fprintf(report, "DRY\t%s\n", relPath)
		return nil
	}

	// 读取源文件
	data, err := src.LoadFile(relPath)
	if err != nil {
		atomic.AddInt64(&st.failed, 1)
		fmt.Fprintf(report, "FAIL\t%s\t%v\n", relPath, err)
		return fmt.Errorf("读取失败: %w", err)
	}

	// 写入目标
	if err := dst.SaveFile(relPath, data); err != nil {
		atomic.AddInt64(&st.failed, 1)
		fmt.Fprintf(report, "FAIL\t%s\t%v\n", relPath, err)
		return fmt.Errorf("写入失败: %w", err)
	}

	atomic.AddInt64(&st.success, 1)
	atomic.AddInt64(&st.bytes, int64(len(data)))
	fmt.Fprintf(report, "OK\t%s\t%d\n", relPath, len(data))
	return nil
}
