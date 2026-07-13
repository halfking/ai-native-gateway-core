// Command env-injector decrypts SOPS-encrypted .env.<target>.enc files and
// emits shell-compatible export statements for use with eval.
//
// Usage:
//
//	eval "$(env-injector inject --target=252)"
//	eval "$(env-injector inject --target=kaixuan-1)"
//	env-injector list
//	env-injector verify --target=252
//	env-injector encrypt --target=252
//
// Commands:
//
//	inject   Decrypt .env.<target>.enc and emit export statements
//	list     List available targets and SSH key paths
//	verify   Verify a target's .enc file is decryptable (no values shown)
//	encrypt  Encrypt a plaintext .env.<target> to .enc using SOPS
//	version  Print version
//
// The SOPS binary (v3.13+) and an age private key must be available.
// See .sops.yaml for the encryption configuration.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kaixuan/llm-gateway-go/envinjector"
)

const version = "1.0.0"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(64)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "inject":
		cmdInject(args)
	case "list":
		cmdList(args)
	case "verify":
		cmdVerify(args)
	case "encrypt":
		cmdEncrypt(args)
	case "version", "-v", "--version":
		fmt.Printf("env-injector %s\n", version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(64)
	}
}

func cmdInject(args []string) {
	fs := flag.NewFlagSet("inject", flag.ExitOnError)
	target := fs.String("target", "", "deployment target alias (252, 154, kaixuan-1, etc.)")
	format := fs.String("format", "eval", "output format: eval, json, dotenv")
	dryRun := fs.Bool("dry-run", false, "check decryptability without emitting values")
	fs.Parse(args)

	if *target == "" {
		fmt.Fprintln(os.Stderr, "error: --target is required")
		os.Exit(64)
	}

	repoRoot := findRepoRoot()
	inj := envinjector.New(repoRoot)

	out, err := inj.Inject(*target, *format, *dryRun)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Print(out)
}

func cmdList(args []string) {
	repoRoot := findRepoRoot()
	inj := envinjector.New(repoRoot)
	fmt.Print(inj.ListTargets())
}

func cmdVerify(args []string) {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	target := fs.String("target", "", "deployment target alias")
	fs.Parse(args)

	if *target == "" {
		fmt.Fprintln(os.Stderr, "error: --target is required")
		os.Exit(64)
	}

	repoRoot := findRepoRoot()
	inj := envinjector.New(repoRoot)

	if err := inj.Verify(*target); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("OK: %s decrypts successfully\n", *target)
}

func cmdEncrypt(args []string) {
	fs := flag.NewFlagSet("encrypt", flag.ExitOnError)
	target := fs.String("target", "", "deployment target alias")
	input := fs.String("input", "", "plaintext .env file (default: .env.<target>)")
	fs.Parse(args)

	if *target == "" {
		fmt.Fprintln(os.Stderr, "error: --target is required")
		os.Exit(64)
	}

	repoRoot := findRepoRoot()
	inj := envinjector.New(repoRoot)

	if err := inj.EncryptTarget(*target, *input); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("OK: encrypted to .env.%s.enc\n", envinjector.ResolveAlias(*target))
}

func usage() {
	fmt.Fprint(os.Stderr, `env-injector — SOPS credential injection for deployment targets

Usage:
  env-injector inject --target=<alias> [--format=eval|json|dotenv] [--dry-run]
  env-injector list
  env-injector verify --target=<alias>
  env-injector encrypt --target=<alias> [--input=<file>]
  env-injector version

Targets:
  252         Alibaba Cloud pre-production (legacy: 184)
  154         Production (legacy: 71)
  245         Registry / pre-production gate
  kaixuan-1   Company k3s cluster

Examples:
  eval "$(env-injector inject --target=252)"
  env-injector verify --target=kaixuan-1
  env-injector inject --target=252 --format=json
`)
}

// findRepoRoot walks up from the current directory to find the repository
// root (identified by .sops.yaml or go.mod).
func findRepoRoot() string {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, ".sops.yaml")); err == nil {
			return dir
		}
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "."
}
