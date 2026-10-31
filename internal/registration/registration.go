// Package registration wires this gateway process into the goacos service
// registry, so that peer services can discover it without a hard-coded IP.
//
// 口径（与 memora / openpocket / RedClaw 平台 8 个 / marketing / smm-web
// **同口径** —— 同口径不是巧合，是跨仓判据表存在的前提：每个仓各推导一次，
// 就各错一次）：
//
//  1. **默认关。** GOACOS_ENABLED 读不到、或为假值 ⇒ 不注册。
//     「装上 SDK 就开始往注册中心写东西」不是我们要的默认值。
//
//  2. **地址必须显式给。** GOACOS_IP 不给就不注册。
//     不猜：多网卡机器（netbird + 内网 + docker bridge）上探测到的地址常常是
//     peer 拨不通的那个；发布一条拨不通的实例，比不发布更糟。
//
//  3. **namespace 默认 public。** 三套 SDK 都不给这个默认值（Go 绑定的
//     Config.NamespaceID 默认空串），而空串会让同一个服务在两套注册面里被
//     拆成两个 namespace ⇒ 默认值由包装层给。
//
//  4. **名字跟声明面走。** llm-gateway —— 与
//     service-registration.itestu.json 的 serviceName 同名。
//
//  5. **端口按「面」区分来源**（这一类与前 12 个服务**不同**）：
//
//     **端口是「面」的属性，不是「服务」的属性。** 同一个二进制
//     （llm-gateway-go）同时跑在两种**结构不同**的面上：
//
//     (a) **没有映射层的面** —— 154 / 245 走 systemd（**蓝绿切流**）：
//     进程直接 bind 在宿主上 ⇒ 对外端口 ≡ 进程 bind 的端口。
//     活跃端口在 8781 / 8782 之间来回换（AGENTS.md §「端口：必须现场确认，
//     不要假设」记了三次对调：2026-10-10 两台都在 :8782；2026-10-11 245
//     漂到 :8782；2026-10-15 两台又对调）。
//     ⇒ 这一面**不许**有端口变量：若通告端口来自任何**部署面变量**，
//     蓝绿切流当天注册面里就会留着一条「已经不再监听的端口」的实例行
//     ⇒ peer 照着拨 ⇒ connection refused ⇒ 表现为「注册面 healthy、
//     真实请求 502」。
//     ⇒ 端口只能来自**进程实际 bind 的那个端口**（cfg.Listen）——
//     结构上不可能与真实监听漂移。
//
//     (b) **有映射层的面** —— 184 的 k3s（Service type NodePort，
//     nodePort 10023 → 容器 8781）：容器端口只在集群内可见，peer 拨的是
//     **节点上的 10023**（域名的实际路径也是 56 nginx → 184:10023）。
//     ⇒ 这一面**必须**由部署面显式声明发布端口
//     （GOACOS_PUBLISHED_PORT），否则代码只知道容器内那个 8781
//     ⇒ 通告一个 peer 拨不通的端口 ⇒ 同一个「注册面 healthy、
//     真实请求 502」。
//     ⇒ 而这一面的 nodePort 是**清单里的字面量**，不会像蓝绿那样漂
//     ⇒ 把它写成部署面变量是**安全**的。
//
//     ⇒ 两个面的规则**不一样**，但它们由**同一个变量**区分：
//     **GOACOS_PUBLISHED_PORT 没给 ⇒ 走 (a)（从 bind 派生）；
//     给了 ⇒ 走 (b)（用部署面声明的发布端口）。**
//     ⚠ 这一条绝不能做成「两个变量」：同一份二进制跑在两种面上，
//     两个变量就分不清「这个面是哪种」⇒ 迟早有一次装错。
//
//     代价：(a) 这一类的「对外端口」= 进程监听端口，**不是**宿主发布端口
//     （容器化服务是后者，因为那里有一层 ports: 映射）。判据表按
//     face_kind 区分这两类，见 myvpn/scripts/tests/
//     test_goacos_registration_contract.py 的 T11 / T16。
package registration

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/halfking/goacos/sdk/registry"
)

// ServiceName is the name this service registers under. It is the **authoritative**
// name: the same string appears in the declaration plane
// (service-registration.itestu.json → serviceName) and in the application model.
const ServiceName = "llm-gateway"

// DefaultNamespace is the namespace used when GOACOS_NAMESPACE is unset.
// Same wording as the reconcile side (goacos-reconcile.sh's GOACOS_NS).
const DefaultNamespace = "public"

// DefaultServerAddr is the registry address used when GOACOS_SERVER_ADDR is unset.
const DefaultServerAddr = "http://127.0.0.1:8848"

// PublishedPortEnv is the **face-level port declaration**.
//
// 口径：端口是「面」的属性，不是「服务」的属性。
//
//	· **没有**端口映射层的面（systemd 直连 / 蓝绿）：对外端口 ≡ 进程 bind
//	  的端口 ⇒ 这一面**不许**声明它 —— 蓝绿切流当天变量就过期了。
//	· **有**端口映射层的面（容器化 / k8s NodePort）：peer 拨的是**宿主
//	  发布端口**，而代码只知道容器内那个 ⇒ 这一面**必须**声明它。
//
// ⇒ **没给 ⇒ 从 bind 派生；给了 ⇒ 用它。**
//
// ⚠ 名字里是 PUBLISHED 而不是 PORT：`GOACOS_PORT` 是全队「容器化服务的宿主
//
//	发布端口」那个名字，而 llm-gateway 这一族**明确禁止**读它（154 / 245
//	的端口必须来自 bind —— 蓝绿切流当天任何变量都会过期）。
//	另起一个名字，两族的规则才不会互相污染。
const PublishedPortEnv = "GOACOS_PUBLISHED_PORT"

// DeregisterTimeout bounds the deregistration call during shutdown.
//
// ⚠ The SDK's Client.Stop skips the deregistration when the context it is given
// is already done ("the server evicts ephemeral instances on its own"). The
// gateway's own ctx is **already cancelled** at that point (it is the
// signal-cancellable one), so we must hand Stop a **fresh** context — otherwise
// the instance row is left behind until the heartbeat window expires.
const DeregisterTimeout = 3 * time.Second

// Settings is everything needed to register this process.
type Settings struct {
	ServerAddr  string
	IP          string
	Port        int
	ServiceName string
	NamespaceID string
	GroupName   string
	ClusterName string
	Weight      float64
	Metadata    map[string]string
	Username    string
	Password    string
	Token       string
}

// Outcome is the result of reading the settings.
//
// Enabled=false always carries a Reason: every "why did it not register" must be
// sayable in the log, otherwise an operator looking at "the service did not
// register" can only guess.
type Outcome struct {
	Enabled  bool
	Reason   string
	Settings Settings
}

// envBool parses the spellings that appear in compose files, .env files and
// shell exports.
//
// **Aligned with the Go SDK's envBool character for character**: 1/t/true/yes/y/on
// ⇒ on; 0/f/false/no/n/off ⇒ off; anything else is an **error**, and an error
// here means "do not register" (not "register and hope").
//
// The same variable is read by several services in this fleet. If each wrapper
// accepted its own spellings, an operator writing GOACOS_ENABLED=yes would turn
// on three services and leave the fourth off — with nothing in the logs to show
// why.
func envBool(getenv func(string) string, key string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return fallback, nil
	}
	switch strings.ToLower(raw) {
	case "1", "t", "true", "yes", "y", "on":
		return true, nil
	case "0", "f", "false", "no", "n", "off":
		return false, nil
	}
	return false, fmt.Errorf("%s=%q is not a boolean", key, raw)
}

// ListenPort extracts the TCP port from a listen address such as ":8781" or
// "0.0.0.0:8781".
//
// ⚠ 这是「**没有**端口映射层」的那一面（154 / 245 / 211）通告端口的**唯一**
//
//	  来源（见包注释）：
//
//		它是进程真正 bind 的那个端口，所以蓝绿切流换端口时它不可能与现实漂移。
//
// ⚠ 但它**不是**有映射层那一面的来源：184 的 k8s 面上，peer 拨的是节点上
//
//	的 NodePort（10023），不是容器内那个 8781 ⇒ 那一面由
//	PublishedPortEnv 声明（见 PublishedPort）。
func ListenPort(listenAddr string) (int, error) {
	addr := strings.TrimSpace(listenAddr)
	if addr == "" {
		return 0, fmt.Errorf("listen address is empty")
	}
	_, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, fmt.Errorf("cannot parse listen address %q: %w", listenAddr, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return 0, fmt.Errorf("listen address %q has a non-numeric port: %w", listenAddr, err)
	}
	if port <= 0 || port > 65535 {
		return 0, fmt.Errorf("listen address %q has an out-of-range port %d", listenAddr, port)
	}
	return port, nil
}

// PublishedPort reads the **face-level port declaration**.
//
// 返回 (port, declared, err)：
//
//   - declared == false ⇒ 这个面**没有**声明 ⇒ 调用方回退到「从 bind 派生」
//     （那是 154 / 245 / 211 那一类面的行为）。
//   - declared == true  ⇒ 用这个值（那是 184 的 k8s NodePort 那一类面）。
//   - err != nil        ⇒ 声明了但不是合法端口 ⇒ **不注册**，而且
//     **不回退**到 bind 端口。
//
// ⚠ 为什么非法值**不回退**：回退 = 把「容器内那个端口」当成对外端口
//
//	= 通告一个 peer 拨不通的端口 = 「注册面 healthy、真实请求 502」。
//	宁可**不注册**（peer 看不到这个实例 ⇒ 流量落到别的实例上），
//	也不要注册一个错的 —— 这与「端口缺失 ⇒ 不注册，绝不回退到容器内
//	监听端口」是同一条纪律。
func PublishedPort(getenv func(string) string) (int, bool, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	raw := strings.TrimSpace(getenv(PublishedPortEnv))
	if raw == "" {
		return 0, false, nil
	}
	port, err := strconv.Atoi(raw)
	if err != nil {
		return 0, true, fmt.Errorf("%s=%q 不是一个端口号", PublishedPortEnv, raw)
	}
	if port <= 0 || port > 65535 {
		return 0, true, fmt.Errorf("%s=%d 超出端口范围（1..65535）", PublishedPortEnv, port)
	}
	return port, true, nil
}

// ParseMetadata parses the fleet's "k=v,k=v" metadata format.
//
// A segment that cannot be parsed is skipped rather than fatal: metadata is
// descriptive, and a typo in it must not stop the service from registering.
func ParseMetadata(raw string) map[string]string {
	out := map[string]string{}
	for _, segment := range strings.Split(raw, ",") {
		item := strings.TrimSpace(segment)
		if item == "" {
			continue
		}
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out[key] = strings.TrimSpace(value)
	}
	return out
}

// ReadSettings reads the registration settings from the environment.
//
// listenAddr is the address this process is about to bind (cfg.Listen).
//
// ⚠ **端口按「面」区分来源**（包注释 §5）：
//   - 部署面给了 GOACOS_PUBLISHED_PORT ⇒ 用它（**有映射层**的面：
//     容器化 / k8s NodePort —— peer 拨的是宿主发布端口，不是容器内端口）。
//   - 没给 ⇒ 从 listenAddr 派生（**没有映射层**的面：systemd 直连 ——
//     对外端口 ≡ 进程 bind 的端口，蓝绿切流当天也不可能漂）。
//
// ⚠ 给了但值非法 ⇒ **不注册**，且**不回退**到 bind 端口。
func ReadSettings(getenv func(string) string, listenAddr string) Outcome {
	if getenv == nil {
		getenv = os.Getenv
	}

	enabled, err := envBool(getenv, "GOACOS_ENABLED", false)
	if err != nil {
		return Outcome{Reason: err.Error() + " —— 不注册（Go 侧把非法值当错误处理，口径一致）"}
	}
	if !enabled {
		return Outcome{Reason: "GOACOS_ENABLED 未开启（默认关）"}
	}

	ip := strings.TrimSpace(getenv("GOACOS_IP"))
	if ip == "" {
		return Outcome{Reason: "没有 GOACOS_IP —— 不采用自动探测出来的地址：多网卡机器上探测到的地址常常是 peer 拨不通的那个"}
	}

	port, declared, err := PublishedPort(getenv)
	if err != nil {
		return Outcome{Reason: "面级端口声明不可用（绝不用 bind 端口顶替 —— 那会通告一个 peer 拨不通的端口）：" + err.Error()}
	}
	if !declared {
		port, err = ListenPort(listenAddr)
		if err != nil {
			return Outcome{Reason: "监听地址不可解析（通告端口必须来自进程实际 bind 的端口）：" + err.Error()}
		}
	}

	settings := Settings{
		ServerAddr:  strings.TrimSpace(getenv("GOACOS_SERVER_ADDR")),
		IP:          ip,
		Port:        port,
		ServiceName: strings.TrimSpace(getenv("GOACOS_SERVICE_NAME")),
		NamespaceID: strings.TrimSpace(getenv("GOACOS_NAMESPACE")),
		GroupName:   strings.TrimSpace(getenv("GOACOS_GROUP")),
		ClusterName: strings.TrimSpace(getenv("GOACOS_CLUSTER")),
		Metadata:    ParseMetadata(getenv("GOACOS_METADATA")),
		Username:    strings.TrimSpace(getenv("GOACOS_USERNAME")),
		Password:    strings.TrimSpace(getenv("GOACOS_PASSWORD")),
		Token:       strings.TrimSpace(getenv("GOACOS_TOKEN")),
	}
	if settings.ServerAddr == "" {
		settings.ServerAddr = DefaultServerAddr
	}
	if settings.ServiceName == "" {
		settings.ServiceName = ServiceName
	}
	if settings.NamespaceID == "" {
		settings.NamespaceID = DefaultNamespace
	}
	if settings.Weight, err = strconv.ParseFloat(strings.TrimSpace(getenv("GOACOS_WEIGHT")), 64); err != nil {
		settings.Weight = 1.0
	}
	if settings.Weight <= 0 {
		settings.Weight = 1.0
	}
	return Outcome{Enabled: true, Settings: settings}
}

// sdkConfig maps our settings onto the SDK's configuration.
//
// ⚠ Enabled is passed **explicitly**: the SDK's zero value is false, so leaving
// it out would silently disable registration — and the reverse mistake (the
// Python SDK defaults to true) is exactly why every wrapper states its own
// default out loud.
func sdkConfig(s Settings) registry.Config {
	return registry.Config{
		Enabled:           true,
		ServerAddr:        s.ServerAddr,
		ServiceName:       s.ServiceName,
		NamespaceID:       s.NamespaceID,
		GroupName:         s.GroupName,
		ClusterName:       s.ClusterName,
		IP:                s.IP,
		Port:              s.Port,
		Weight:            s.Weight,
		Metadata:          s.Metadata,
		Username:          s.Username,
		Password:          s.Password,
		Token:             s.Token,
		HeartbeatInterval: 5 * time.Second,
		RequestTimeout:    5 * time.Second,
	}
}

// Start registers this process if registration is enabled.
//
// It never blocks startup and never panics: a registry that is down,
// misconfigured or not yet deployed must not stop the gateway from serving.
// Returns nil when registration is off or could not be configured — the caller
// passes that nil straight back to Stop.
func Start(ctx context.Context, listenAddr string, logger *slog.Logger) *registry.Client {
	if logger == nil {
		logger = slog.Default()
	}
	outcome := ReadSettings(os.Getenv, listenAddr)
	if !outcome.Enabled {
		logger.Info("goacos registration skipped", "reason", outcome.Reason)
		return nil
	}
	client := registry.NewClient(sdkConfig(outcome.Settings), logger)
	client.Start(ctx)
	logger.Info("goacos registration started",
		"service", outcome.Settings.ServiceName,
		"ip", outcome.Settings.IP,
		"port", outcome.Settings.Port,
		"namespace", outcome.Settings.NamespaceID)
	return client
}

// Stop deregisters this process.
//
// Must be called **before the listener closes**: while the instance row is
// still in the registry, peers keep dialing a process that is no longer
// accepting requests.
//
// Safe to call with a nil client (registration was off).
func Stop(client *registry.Client) {
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), DeregisterTimeout)
	defer cancel()
	if err := client.Stop(ctx); err != nil {
		// Deregistration is best-effort: the server evicts ephemeral instances
		// that stop beating anyway, so a registry that is down must not turn a
		// shutdown into a hang. It only turns a short window of phantom routing
		// into a slightly longer one.
		slog.Warn("goacos deregister failed; the registry will evict this instance on heartbeat timeout",
			"error", err)
	}
}

// StopAsync starts the deregistration without blocking the caller's shutdown
// sequence.
//
// ⚠ **Why not synchronous**: this gateway's systemd unit has a tight stop
// budget, and that budget is **already fully allocated** —
//
//	srv.Shutdown gets 23s for the drain and the worker cleanup needs the
//	remaining ~12s (see the 2026-08-19 note in cmd/gateway/main.go).
//
//	⚠ The budget has **two faces** and only one of them is in the repo:
//	  · deploy/llm-gateway-go.service and deploy/llmgo-245.service say
//	    `TimeoutStopSec=25s` (tracked, in-repo);
//	  · the live hosts raise it to 35s through a systemd **drop-in**, and
//	    cmd/gateway/main.go's 2026-08-19 note refers to that drop-in.
//	    That drop-in is **not** in the repo ⇒ the live budget is host-side
//	    state we cannot read from here.
//
//	⇒ Under the *smaller* of the two budgets a synchronous deregistration
//	would eat into the worker's share and could get the process SIGKILLed,
//	leaving leases and DB connections dirty. That is a worse failure than a
//	peer dialing this instance a few milliseconds too late.
//
// ⇒ The DELETE is therefore **issued before** the listener closes (peers stop
//
//	being routed here within milliseconds) but it is not waited for. If the
//	process exits before the DELETE lands, the registry evicts the instance on
//	heartbeat timeout (15s unhealthy / 30s removed) anyway.
func StopAsync(client *registry.Client) {
	if client == nil {
		return
	}
	go Stop(client)
}
