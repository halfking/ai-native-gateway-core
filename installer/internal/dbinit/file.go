package dbinit

import "os"

// openFile 抽象出文件打开，便于测试时 mock
func openFile(path string) (*os.File, error) {
	return os.Open(path)
}
