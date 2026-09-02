package databricks_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marmot1024/metricspire/internal/adapter/databricks"
	"github.com/marmot1024/metricspire/internal/compiler"
	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/planner"
)

func TestCompileProducesParameterizedDatabricksSQL(t *testing.T) {
	t.Parallel()
	plan := buildPlan(t, nil, nil)
	statement, err := databricks.Compile(plan)
	if err != nil {
		t.Fatal(err)
	}
	expected := "SELECT\n" +
		"  date_trunc('DAY', from_utc_timestamp(t0.`order_ts`, :timezone_1)) AS `order_date`,\n" +
		"  t1.`segment` AS `customer_segment`,\n" +
		"  (SUM(t0.`gross_amount`) / NULLIF(SUM(t0.`order_count`), 0)) AS `average_order_value`,\n" +
		"  (SUM(t0.`refunded_amount`) / NULLIF(SUM(t0.`gross_amount`), 0)) AS `refund_rate`\n" +
		"FROM `demo`.`public`.`orders` AS t0\n" +
		"LEFT JOIN `demo`.`public`.`customers` AS t1 ON t0.`customer_id` = t1.`customer_id`\n" +
		"WHERE t0.`order_ts` >= :time_start_2 AND t0.`order_ts` < :time_end_3\n" +
		"  AND t1.`region` IN (:filter_4, :filter_5)\n" +
		"GROUP BY date_trunc('DAY', from_utc_timestamp(t0.`order_ts`, :timezone_1)), t1.`segment`\n" +
		"ORDER BY `average_order_value` DESC\n" +
		"LIMIT 100"
	if statement.SQL != expected {
		t.Fatalf("SQL differs\n--- got ---\n%s\n--- want ---\n%s", statement.SQL, expected)
	}
	if statement.PhysicalFingerprint != plan.Fingerprint || len(statement.Parameters) != 5 {
		t.Fatalf("statement metadata = %#v", statement)
	}
	for i, expectedType := range []string{"STRING", "TIMESTAMP", "TIMESTAMP", "STRING", "STRING"} {
		if statement.Parameters[i].Type != expectedType || statement.Parameters[i].Value == nil {
			t.Fatalf("parameter[%d] = %#v", i, statement.Parameters[i])
		}
	}
	if *statement.Parameters[3].Value != "east" || *statement.Parameters[4].Value != "south" {
		t.Fatalf("filter parameters = %#v", statement.Parameters[3:])
	}
}

func TestCompileSupportsCommonAggregatesAndConstrainedMetricFilters(t *testing.T) {
	t.Parallel()
	mutateSource := func(source *model.SemanticSource) {
		for i := range source.Spec.Datasets {
			if source.Spec.Datasets[i].Name == "orders" {
				source.Spec.Datasets[i].Fields = append(source.Spec.Datasets[i].Fields,
					model.Field{Name: "status", DataType: model.DataTypeString})
			}
		}
		base := model.Metric{
			Entity: "orders", Kind: model.MetricAggregate, AllowedDimensions: []string{"customer_segment", "order_date"},
			TimeDimension: "order_date", Verification: model.Verification{Status: model.VerificationVerified},
		}
		metrics := []model.Metric{
			{Name: "completed_orders", ValueType: model.DataTypeInteger, Expression: model.Expression{
				Op: model.OpCount, Field: "orders.order_id", Filters: []model.MetricFilter{{
					Field: "orders.status", Operator: model.MetricFilterIn, Values: []string{"refunded", "completed"},
				}},
			}},
			{Name: "unique_customers", ValueType: model.DataTypeInteger, Expression: model.Expression{Op: model.OpCountDistinct, Field: "orders.customer_id"}},
			{Name: "average_revenue", ValueType: model.DataTypeDecimal, Expression: model.Expression{Op: model.OpAverage, Field: "orders.gross_amount"}},
			{Name: "minimum_revenue", ValueType: model.DataTypeDecimal, Expression: model.Expression{Op: model.OpMinimum, Field: "orders.gross_amount"}},
			{Name: "maximum_revenue", ValueType: model.DataTypeDecimal, Expression: model.Expression{Op: model.OpMaximum, Field: "orders.gross_amount"}},
		}
		for i := range metrics {
			metrics[i].Entity = base.Entity
			metrics[i].Kind = base.Kind
			metrics[i].AllowedDimensions = append([]string(nil), base.AllowedDimensions...)
			metrics[i].TimeDimension = base.TimeDimension
			metrics[i].Verification = base.Verification
		}
		source.Spec.Metrics = append(source.Spec.Metrics, metrics...)
	}
	mutateBinding := func(binding *model.SourceBinding) {
		for i := range binding.Datasets {
			if binding.Datasets[i].Name == "orders" {
				binding.Datasets[i].Fields = append(binding.Datasets[i].Fields, model.FieldBinding{Name: "status", Column: "status"})
			}
		}
	}
	plan := buildPlan(t, mutateSource, mutateBinding)
	statement, err := databricks.Compile(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		"COUNT(CASE WHEN t0.`status` IN (:metric_filter_1, :metric_filter_2) THEN t0.`order_id` ELSE NULL END) AS `completed_orders`",
		"COUNT(DISTINCT t0.`customer_id`) AS `unique_customers`",
		"AVG(t0.`gross_amount`) AS `average_revenue`",
		"MIN(t0.`gross_amount`) AS `minimum_revenue`",
		"MAX(t0.`gross_amount`) AS `maximum_revenue`",
	} {
		if !strings.Contains(statement.SQL, fragment) {
			t.Errorf("SQL does not contain %q:\n%s", fragment, statement.SQL)
		}
	}
	if len(statement.Parameters) < 2 || *statement.Parameters[0].Value != "completed" || *statement.Parameters[1].Value != "refunded" {
		t.Fatalf("metric filter parameters = %#v", statement.Parameters)
	}
}

func TestCompileRejectsTamperedPlan(t *testing.T) {
	t.Parallel()
	plan := buildPlan(t, nil, nil)
	plan.Limit++
	_, err := databricks.Compile(plan)
	var problem *model.Problem
	if !errors.As(err, &problem) || problem.Code != "fingerprint_mismatch" {
		t.Fatalf("error = %v", err)
	}
}

func buildPlan(t *testing.T, mutateSource func(*model.SemanticSource), mutateBinding func(*model.SourceBinding)) model.PhysicalPlan {
	t.Helper()
	root := filepath.Join("..", "..", "..", "examples", "orders")
	var source model.SemanticSource
	read(t, filepath.Join(root, "model.yaml"), &source)
	if mutateSource != nil {
		mutateSource(&source)
	}
	manifest, err := compiler.Compile(source)
	if err != nil {
		t.Fatal(err)
	}
	var policySource model.PolicySource
	read(t, filepath.Join(root, "policy.yaml"), &policySource)
	policySource.ManifestFingerprint = ""
	bundle, err := compiler.CompilePolicy(policySource, manifest)
	if err != nil {
		t.Fatal(err)
	}
	var context model.RequestContext
	var query model.SemanticQuery
	var binding model.SourceBinding
	read(t, filepath.Join(root, "context.json"), &context)
	read(t, filepath.Join(root, "query.json"), &query)
	read(t, filepath.Join(root, "binding.json"), &binding)
	binding.ManifestFingerprint = manifest.Fingerprint
	binding.Engine = databricks.EngineName
	if mutateBinding != nil {
		mutateBinding(&binding)
	}
	if mutateSource != nil {
		query.Metrics = []string{"completed_orders", "unique_customers", "average_revenue", "minimum_revenue", "maximum_revenue"}
		query.GroupBy = []string{"customer_segment"}
		query.TimeGrouping = nil
		query.Filters = nil
		query.OrderBy = nil
	}
	logical, err := planner.BuildLogical(manifest, bundle, context, query)
	if err != nil {
		t.Fatal(err)
	}
	physical, err := planner.BuildPhysical(manifest, logical, binding, databricks.Capabilities())
	if err != nil {
		t.Fatal(err)
	}
	return physical
}

func read(t *testing.T, path string, target any) {
	t.Helper()
	if err := contractio.ReadFile(path, target); err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
}
