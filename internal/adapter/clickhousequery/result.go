package clickhousequery

import (
	"context"
	"github.com/marmot1024/metricspire/internal/adapter/onlineaggregate"
	"github.com/marmot1024/metricspire/internal/model"
)

func (e *QueryEngine) result(ctx context.Context, p model.PhysicalPlan, s statement, rows [][]*string, contract string) (model.ExecutionSnapshot, error) {
	return (onlineaggregate.Config{MaxDataAge: e.config.MaxDataAge, MaxBytes: e.config.MaxBytes, Now: e.now}).Result(ctx, p, s.dimensions, s.fields, rows, contract)
}
