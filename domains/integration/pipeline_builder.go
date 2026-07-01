// Package integration 集成层：把所有领域 Hook 拼装成完整的 RequestPipeline。
//
// 这是 Phase 1 的核心成果：identity / session / authentication 三个
// 领域的 Hook 通过 RequestPipeline 串联起来，可以被 transport 层
// 或 main 装配到生产请求流程。
package integration

import (
	"github.com/kaixuan/llm-gateway-go/domains/authentication" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/identity"       //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"       //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/session"        //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// BuildRequestPipeline 构造完整的请求管道。
// 包含两个 stage：
//  1. authentication (sequential)
//  2. pre_routing (parallel: identity + session)
func BuildRequestPipeline(
	identityBuilder *identity.IdentityBuilder,
	apiKeyVerifier *authentication.Verifier,
	sessionStore session.SessionStore,
) *pipeline.RequestPipeline {
	p := pipeline.NewRequestPipeline()
	sticky := session.NewStickyRouter(sessionStore)

	// Stage 1: Authentication
	p.AddStage(&pipeline.PipelineStage{
		Name:  "authentication",
		Phase: pipeline.PhaseAuthentication,
		Mode:  pipeline.ModeSequential,
		Hooks: []pipeline.Hook{
			authentication.NewAPIKeyAuthHook(apiKeyVerifier),
		},
	})

	// Stage 2: Pre-Routing (identity + session 并行)
	hooks := []pipeline.Hook{}
	if identityBuilder != nil {
		hooks = append(hooks, identity.NewClientIdentityHook())
	}
	if sessionStore != nil {
		hooks = append(hooks, session.NewSessionLoaderHook(sessionStore, sticky))
	}
	if len(hooks) > 0 {
		p.AddStage(&pipeline.PipelineStage{
			Name:  "pre_routing",
			Phase: pipeline.PhasePreRouting,
			Mode:  pipeline.ModeParallel,
			Hooks: hooks,
		})
	}

	return p
}
