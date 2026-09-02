// Package application contains transport-independent product use cases.
package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/compiler"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/planner"
)

type ReleaseReader interface {
	GetActiveRelease(context.Context, string, string) (catalog.Release, error)
}

type QueryEngine interface {
	Capabilities() model.EngineCapabilities
	Execute(context.Context, model.PhysicalPlan) (model.ExecutionSnapshot, error)
}

type QueryService struct {
	releases ReleaseReader
	engine   QueryEngine
}

type QueryInput struct {
	Namespace string
	ModelName string
	Context   model.RequestContext
	Query     model.SemanticQuery
	Policy    model.PolicySource
	Binding   model.SourceBinding
}

type QueryOutput struct {
	Release   catalog.Release         `json:"release"`
	Logical   model.LogicalPlan       `json:"logical_plan"`
	Physical  model.PhysicalPlan      `json:"physical_plan"`
	Execution model.ExecutionSnapshot `json:"execution"`
}

func NewQueryService(releases ReleaseReader, engine QueryEngine) (*QueryService, error) {
	if releases == nil || engine == nil {
		return nil, errors.New("release reader and query engine are required")
	}
	return &QueryService{releases: releases, engine: engine}, nil
}

// ExecuteActive resolves the active release on the server. The caller cannot
// select an unpublished or stale manifest fingerprint in SemanticQuery.
func (s *QueryService) ExecuteActive(ctx context.Context, input QueryInput) (QueryOutput, error) {
	if input.Namespace == "" || input.ModelName == "" {
		return QueryOutput{}, errors.New("namespace and model name are required")
	}
	release, err := s.releases.GetActiveRelease(ctx, input.Namespace, input.ModelName)
	if err != nil {
		return QueryOutput{}, fmt.Errorf("get active release: %w", err)
	}
	bundle, err := compiler.CompilePolicy(input.Policy, release.Manifest)
	if err != nil {
		return QueryOutput{}, fmt.Errorf("compile policy for active release: %w", err)
	}
	logical, err := planner.BuildLogical(release.Manifest, bundle, input.Context, input.Query)
	if err != nil {
		return QueryOutput{}, fmt.Errorf("build logical plan: %w", err)
	}
	physical, err := planner.BuildPhysical(release.Manifest, logical, input.Binding, s.engine.Capabilities())
	if err != nil {
		return QueryOutput{}, fmt.Errorf("build physical plan: %w", err)
	}
	execution, err := s.engine.Execute(ctx, physical)
	if err != nil {
		return QueryOutput{Release: release, Logical: logical, Physical: physical, Execution: execution}, err
	}
	return QueryOutput{Release: release, Logical: logical, Physical: physical, Execution: execution}, nil
}
