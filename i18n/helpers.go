package i18n

import (
	"encoding/json"
	"io/fs"
	"os"
)

// jsonUnmarshal is the JSON decoder registered with go-i18n. Indirected
// through a variable so tests can substitute a decoder if needed.
func jsonUnmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

// osFS adapts the os package to fs.FS/fs.ReadDir so loadDir can walk a real
// directory of external locale overrides. It only implements what loadDir uses
// (WalkDir + ReadFile), keeping the dependency surface minimal.
type osFS struct{}

func (osFS) Open(name string) (fs.File, error)          { return os.Open(name) }
func (osFS) ReadDir(name string) ([]fs.DirEntry, error) { return os.ReadDir(name) }
func (osFS) ReadFile(name string) ([]byte, error)       { return os.ReadFile(name) }
