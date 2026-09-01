package planner_test

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/marmot1024/metricspire/internal/compiler"
	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/planner"
)

type fixture struct {
	manifest     model.SemanticManifest
	bundle       model.PolicyBundle
	context      model.RequestContext
	query        model.SemanticQuery
	binding      model.SourceBinding
	capabilities model.EngineCapabilities
}

func TestPlansMatchGoldenAndIncludeSafeJoin(t *testing.T) {
	t.Parallel()
	values := loadFixture(t)
	logical, err := planner.BuildLogical(values.manifest, values.bundle, values.context, values.query)
	if err != nil {
		t.Fatal(err)
	}
	physical, err := planner.BuildPhysical(values.manifest, logical, values.binding, values.capabilities)
	if err != nil {
		t.Fatal(err)
	}
	var expectedLogical model.LogicalPlan
	var expectedPhysical model.PhysicalPlan
	read(t, filepath.Join("..", "..", "testdata", "golden", "logical-plan.json"), &expectedLogical)
	read(t, filepath.Join("..", "..", "testdata", "golden", "physical-plan.json"), &expectedPhysical)
	if !reflect.DeepEqual(logical, expectedLogical) {
		t.Fatalf("logical plan differs from golden")
	}
	if !reflect.DeepEqual(physical, expectedPhysical) {
		t.Fatalf("physical plan differs from golden")
	}
	if len(logical.Joins) != 1 || logical.Joins[0].Cardinality != model.CardinalityManyToOne {
		t.Fatalf("safe join was not planned: %#v", logical.Joins)
	}
	for _, dimension := range logical.Dimensions {
		if dimension.Name == "customer_region" && dimension.Output {
			t.Fatal("filter-only dimension was incorrectly marked as grouped output")
		}
	}
}

func TestTimePointsNormalizeToUTCWhileBusinessTimezoneIsPreserved(t *testing.T) {
	t.Parallel()
	values := loadFixture(t)
	baseline, err := planner.BuildLogical(values.manifest, values.bundle, values.context, values.query)
	if err != nil {
		t.Fatal(err)
	}
	values.query.TimeRange.Start = "2026-09-01T00:00:00+08:00"
	values.query.TimeRange.End = "2026-09-08T00:00:00+08:00"
	actual, err := planner.BuildLogical(values.manifest, values.bundle, values.context, values.query)
	if err != nil {
		t.Fatal(err)
	}
	if actual.Fingerprint != baseline.Fingerprint || actual.TimeGrouping.Timezone != "Asia/Shanghai" || actual.TimeRange.Start != "2026-08-31T16:00:00Z" {
		t.Fatalf("time normalization changed semantics: %#v", actual.TimeRange)
	}
}

func TestPlannerRejectsMissingTimezoneCapabilityAndBinding(t *testing.T) {
	t.Parallel()
	t.Run("timezone", func(t *testing.T) {
		values := loadFixture(t)
		values.query.TimeGrouping.Timezone = ""
		_, err := planner.BuildLogical(values.manifest, values.bundle, values.context, values.query)
		assertProblem(t, err, "invalid_time")
	})
	t.Run("weekly grouping without explicit week start", func(t *testing.T) {
		values := loadFixture(t)
		values.query.TimeGrouping.Granularity = model.GrainWeek
		_, err := planner.BuildLogical(values.manifest, values.bundle, values.context, values.query)
		assertProblem(t, err, "invalid_time")
		values.query.TimeGrouping.WeekStart = model.WeekStartMonday
		if _, err := planner.BuildLogical(values.manifest, values.bundle, values.context, values.query); err != nil {
			t.Fatalf("explicit week start was rejected: %v", err)
		}
	})
	t.Run("capability", func(t *testing.T) {
		values := loadFixture(t)
		logical, err := planner.BuildLogical(values.manifest, values.bundle, values.context, values.query)
		if err != nil {
			t.Fatal(err)
		}
		values.capabilities.JoinCardinalities = nil
		_, err = planner.BuildPhysical(values.manifest, logical, values.binding, values.capabilities)
		assertProblem(t, err, "capability_missing")
	})
	t.Run("binding", func(t *testing.T) {
		values := loadFixture(t)
		logical, err := planner.BuildLogical(values.manifest, values.bundle, values.context, values.query)
		if err != nil {
			t.Fatal(err)
		}
		values.binding.Datasets = values.binding.Datasets[1:]
		_, err = planner.BuildPhysical(values.manifest, logical, values.binding, values.capabilities)
		assertProblem(t, err, "binding_missing")
	})
	t.Run("structured resource", func(t *testing.T) {
		values := loadFixture(t)
		logical, err := planner.BuildLogical(values.manifest, values.bundle, values.context, values.query)
		if err != nil {
			t.Fatal(err)
		}
		values.binding.Datasets[0].Resource.Table = "orders;drop"
		_, err = planner.BuildPhysical(values.manifest, logical, values.binding, values.capabilities)
		assertProblem(t, err, "invalid_binding")
	})
	t.Run("file resource cannot carry credentials", func(t *testing.T) {
		values := loadFixture(t)
		logical, err := planner.BuildLogical(values.manifest, values.bundle, values.context, values.query)
		if err != nil {
			t.Fatal(err)
		}
		values.binding.Datasets[0].Resource = model.ResourceRef{Kind: model.ResourceFile, URI: "https://user:secret@example.test/orders.parquet"}
		_, err = planner.BuildPhysical(values.manifest, logical, values.binding, values.capabilities)
		assertProblem(t, err, "invalid_binding")
	})
}

func TestTimeFilterDoesNotImplicitlyGroup(t *testing.T) {
	t.Parallel()
	values := loadFixture(t)
	values.query.GroupBy = []string{"customer_segment"}
	values.query.TimeGrouping = nil
	logical, err := planner.BuildLogical(values.manifest, values.bundle, values.context, values.query)
	if err != nil {
		t.Fatal(err)
	}
	for _, dimension := range logical.Dimensions {
		if dimension.Name == "order_date" && dimension.Output {
			t.Fatal("time filter was incorrectly promoted to a group")
		}
	}
}

func TestSemanticQueryCannotSelfReportIdentityOrRawSQL(t *testing.T) {
	t.Parallel()
	for _, field := range []string{`"context":{"tenant":"other"}`, `"sql":"select * from secret"`} {
		data := []byte(`{"api_version":"metricspire.io/v1alpha1","kind":"SemanticQuery","manifest_fingerprint":"x","metrics":["m"],` + field + `}`)
		var query model.SemanticQuery
		if err := contractio.DecodeJSON(data, &query); err == nil {
			t.Fatalf("query accepted forbidden field %s", field)
		}
	}
}

func TestPhysicalPlannerRejectsTamperedLogicalPlan(t *testing.T) {
	t.Parallel()
	values := loadFixture(t)
	logical, err := planner.BuildLogical(values.manifest, values.bundle, values.context, values.query)
	if err != nil {
		t.Fatal(err)
	}
	logical.Limit++
	_, err = planner.BuildPhysical(values.manifest, logical, values.binding, values.capabilities)
	assertProblem(t, err, "fingerprint_mismatch")
}

func loadFixture(t *testing.T) fixture {
	t.Helper()
	var source model.SemanticSource
	read(t, filepath.Join("..", "..", "examples", "orders", "model.yaml"), &source)
	manifest, err := compiler.Compile(source)
	if err != nil {
		t.Fatal(err)
	}
	var policySource model.PolicySource
	read(t, filepath.Join("..", "..", "examples", "orders", "policy.yaml"), &policySource)
	bundle, err := compiler.CompilePolicy(policySource, manifest)
	if err != nil {
		t.Fatal(err)
	}
	values := fixture{manifest: manifest, bundle: bundle}
	read(t, filepath.Join("..", "..", "examples", "orders", "context.json"), &values.context)
	read(t, filepath.Join("..", "..", "examples", "orders", "query.json"), &values.query)
	read(t, filepath.Join("..", "..", "examples", "orders", "binding.json"), &values.binding)
	read(t, filepath.Join("..", "..", "examples", "orders", "capabilities.json"), &values.capabilities)
	return values
}

func read(t *testing.T, path string, target any) {
	t.Helper()
	if err := contractio.ReadFile(path, target); err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
}

func clone[T any](t *testing.T, value T) T {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result T
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func assertProblem(t *testing.T, err error, code string) {
	t.Helper()
	var problem *model.Problem
	if !errors.As(err, &problem) || problem.Code != code {
		t.Fatalf("error = %v, want %s", err, code)
	}
}
