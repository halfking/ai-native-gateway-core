//go:build ignore

// bcrypt-hash — 从 stdin 读取密码，输出 bcrypt hash（供 sync-admin-password 使用）
package main

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/crypto/bcrypt"
)

func main() {
	pw, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read stdin: %v\n", err)
		os.Exit(1)
	}
	for len(pw) > 0 && (pw[len(pw)-1] == '\n' || pw[len(pw)-1] == '\r') {
		pw = pw[:len(pw)-1]
	}
	if len(pw) == 0 {
		fmt.Fprintln(os.Stderr, "empty password on stdin")
		os.Exit(1)
	}
	hash, err := bcrypt.GenerateFromPassword(pw, bcrypt.DefaultCost)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bcrypt: %v\n", err)
		os.Exit(1)
	}
	fmt.Print(string(hash))
}
