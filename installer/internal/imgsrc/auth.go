package imgsrc

import "os"

// LoadRegistryAuthFromEnv 从环境变量加载 registry 凭证
// 优先级：
//   1. KX_REGISTRY_USERNAME + KX_REGISTRY_PASSWORD
//   2. 默认空（不认证）
func LoadRegistryAuthFromEnv() *RegistryAuth {
	user := os.Getenv("KX_REGISTRY_USERNAME")
	pass := os.Getenv("KX_REGISTRY_PASSWORD")
	if user == "" && pass == "" {
		return nil
	}
	return &RegistryAuth{Username: user, Password: pass}
}

// LoadRegistryFromEnv 从环境变量加载 registry 地址
// 优先级：
//   1. KX_REGISTRY
//   2. 默认 registry.kxpms.cn
func LoadRegistryFromEnv() string {
	if r := os.Getenv("KX_REGISTRY"); r != "" {
		return r
	}
	return "registry.kxpms.cn"
}
