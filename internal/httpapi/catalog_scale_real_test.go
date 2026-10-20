package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/governance"
	"github.com/marmot1024/metricspire/internal/httpapi"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/repository/postgres"
)

func TestCatalogScalePostgresReal(t *testing.T) {
	for _, size := range []int{50, 500, 1000} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			f := setupOnlinePostgres(t)
			cfg, err := pgxpool.ParseConfig(os.Getenv("METRICSPIRE_ONLINE_TEST_DATABASE_URL"))
			if err != nil {
				t.Fatal(err)
			}
			cfg.ConnConfig.RuntimeParams["search_path"] = f.controlSchema
			pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			store, err := postgres.New(pool)
			if err != nil {
				t.Fatal(err)
			}
			source := f.source
			retained := len(source.Spec.Metrics)
			base := source.Spec.Metrics[0]
			for i := 0; i < size-retained; i++ {
				metric := base
				metric.Name = fmt.Sprintf("synthetic_%04d", i)
				metric.ExternalCode = fmt.Sprint(70000 + i)
				metric.DisplayName = metric.Name
				source.Spec.Metrics = append(source.Spec.Metrics, metric)
			}
			draft, err := f.management.SaveDraft(t.Context(), catalog.SaveDraftInput{Namespace: "demo", Source: source, Actor: "synthetic-scale", ExpectedRevision: f.revision})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.management.Publish(t.Context(), "demo", source.Metadata.Name, draft.Revision, "synthetic-scale", "generated catalog scale"); err != nil {
				t.Fatal(err)
			}
			policy := model.PolicySource{APIVersion: model.APIVersion, Kind: model.KindPolicySource, Metadata: model.Metadata{Name: "scale", Version: "1.0.0"}, Tenant: "demo", Rules: []model.PolicyRule{{Name: "scale", Effect: model.EffectAllow, Roles: []string{"analyst"}, Metrics: []string{"*"}, Dimensions: []string{"*"}}}}
			policies := application.PolicyResolverFunc(func(context.Context, application.QueryScope, catalog.Release) (model.PolicySource, error) {
				return policy, nil
			})
			bindings := application.BindingResolverFunc(func(context.Context, application.QueryScope, catalog.Release) (model.SourceBinding, error) {
				return f.binding, nil
			})
			search, err := application.NewCatalogService(store, policies, bindings)
			if err != nil {
				t.Fatal(err)
			}
			gov, err := governance.NewService(store)
			if err != nil {
				t.Fatal(err)
			}
			handler, _, _ := newTestServerWithDependencies(t, httpapi.Config{ControlTimeout: 30 * time.Second}, nil, nil, store, nil, func(d *httpapi.Dependencies) {
				d.Catalog = store
				d.Management = f.management
				d.CatalogSearch = search
				d.Governance = gov
				d.Bindings = bindings
			})
			server := httptest.NewServer(handler)
			defer server.Close()
			checkCatalogScale(t, size, retained, func(path string) *http.Response {
				req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+path, nil)
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Authorization", "Bearer query")
				response, err := server.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				return response
			}, "PostgreSQL metadata / loopback HTTP")
		})
	}
}
