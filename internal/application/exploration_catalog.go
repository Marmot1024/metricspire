package application

import (
	"context"
	"errors"

	"github.com/marmot1024/metricspire/internal/exploration"
	"github.com/marmot1024/metricspire/internal/model"
)

type AnalysisCatalogResolver interface {
	ResolveAnalysisCatalog(context.Context, string, model.RequestContext, string) (exploration.Catalog, error)
}

// ConfiguredAnalysisCatalogs loads reviewed semantic exports once at startup.
// New versions require an explicit reload/restart, not mutable in-flight plans.
type ConfiguredAnalysisCatalogs struct{ catalogs []exploration.Catalog }

func NewConfiguredAnalysisCatalogs(catalogs []exploration.Catalog) (*ConfiguredAnalysisCatalogs, error) {
	if len(catalogs) == 0 {
		return nil, errors.New("analysis catalogs are required")
	}
	seen := map[string]bool{}
	for _, c := range catalogs {
		if err := exploration.ValidateCatalog(c); err != nil {
			return nil, err
		}
		for _, s := range c.Sources {
			key := c.Namespace + "\x00" + c.Tenant + "\x00" + s.Name
			if seen[key] {
				return nil, errors.New("ambiguous analysis source route")
			}
			seen[key] = true
		}
	}
	return &ConfiguredAnalysisCatalogs{catalogs: cloneResolverValue(catalogs)}, nil
}

func (r *ConfiguredAnalysisCatalogs) ResolveAnalysisCatalog(ctx context.Context, namespace string, rc model.RequestContext, source string) (exploration.Catalog, error) {
	if err := ctx.Err(); err != nil {
		return exploration.Catalog{}, err
	}
	for _, c := range r.catalogs {
		if c.Namespace == namespace && c.Tenant == rc.Tenant {
			if _, ok := exploration.SourceIn(c, source); ok {
				return cloneResolverValue(c), nil
			}
		}
	}
	return exploration.Catalog{}, ErrResolutionNotFound
}

func (r *ConfiguredAnalysisCatalogs) ResolveAnalysisSource(ctx context.Context, namespace string, rc model.RequestContext, source string) (AnalysisSourceBinding, error) {
	c, err := r.ResolveAnalysisCatalog(ctx, namespace, rc, source)
	if err != nil {
		return AnalysisSourceBinding{}, err
	}
	s, _ := exploration.SourceIn(c, source)
	return AnalysisSourceBinding{ModelName: c.Metadata.Name, Source: s}, nil
}
