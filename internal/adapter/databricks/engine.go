package databricks

import (
	"context"
	"errors"

	"github.com/marmot1024/metricspire/internal/model"
)

type QueryEngine struct {
	client *Client
}

func NewQueryEngine(client *Client) (*QueryEngine, error) {
	if client == nil {
		return nil, errors.New("Databricks client is required")
	}
	return &QueryEngine{client: client}, nil
}

func (e *QueryEngine) Capabilities() model.EngineCapabilities {
	return Capabilities()
}

func (e *QueryEngine) Execute(ctx context.Context, plan model.PhysicalPlan) (model.ExecutionSnapshot, error) {
	statement, err := Compile(plan)
	if err != nil {
		return model.ExecutionSnapshot{}, err
	}
	return e.client.Execute(ctx, statement)
}
