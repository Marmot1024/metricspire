package databricks_test

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/marmot1024/metricspire/internal/adapter/databricks"
	"github.com/marmot1024/metricspire/internal/compiler"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/planner"
)

func TestTPCHAcceptanceFixtureCompilesToGovernedDatabricksStatement(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..", "..", "testdata", "acceptance", "databricks-tpch")
	var source model.SemanticSource
	var policy model.PolicySource
	var requestContext model.RequestContext
	var query model.SemanticQuery
	var binding model.SourceBinding
	read(t, filepath.Join(root, "model.yaml"), &source)
	read(t, filepath.Join(root, "policy.yaml"), &policy)
	read(t, filepath.Join(root, "context.json"), &requestContext)
	read(t, filepath.Join(root, "query.json"), &query)
	read(t, filepath.Join(root, "binding.json"), &binding)

	manifest, err := compiler.Compile(source)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := compiler.CompilePolicy(policy, manifest)
	if err != nil {
		t.Fatal(err)
	}
	logical, err := planner.BuildLogical(manifest, bundle, requestContext, query)
	if err != nil {
		t.Fatal(err)
	}
	physical, err := planner.BuildPhysical(manifest, logical, binding, databricks.Capabilities())
	if err != nil {
		t.Fatal(err)
	}
	statement, err := databricks.Compile(physical)
	if err != nil {
		t.Fatal(err)
	}
	expectedSQL := "SELECT\n" +
		"  t1.`c_mktsegment` AS `customer_segment`,\n" +
		"  t0.`o_orderstatus` AS `order_status`,\n" +
		"  SUM(t0.`o_totalprice`) AS `gross_revenue`,\n" +
		"  COUNT(t0.`o_orderkey`) AS `order_count`,\n" +
		"  (SUM(t0.`o_totalprice`) / NULLIF(COUNT(t0.`o_orderkey`), 0)) AS `average_order_value`\n" +
		"FROM `samples`.`tpch`.`orders` AS t0\n" +
		"LEFT JOIN `samples`.`tpch`.`customer` AS t1 ON t0.`o_custkey` = t1.`c_custkey`\n" +
		"WHERE t0.`o_orderkey` IN (:filter_1, :filter_2, :filter_3, :filter_4, :filter_5)\n" +
		"GROUP BY t1.`c_mktsegment`, t0.`o_orderstatus`\n" +
		"ORDER BY `average_order_value` DESC\n" +
		"LIMIT 10"
	if statement.SQL != expectedSQL {
		t.Fatalf("TPCH acceptance SQL differs\n--- got ---\n%s\n--- want ---\n%s", statement.SQL, expectedSQL)
	}
	if statement.RowLimit != 10 || len(statement.Parameters) != 5 {
		t.Fatalf("statement budget = row limit %d, parameters %#v", statement.RowLimit, statement.Parameters)
	}
	for index, parameter := range statement.Parameters {
		value := strconv.Itoa(index + 1)
		if parameter.Name != "filter_"+value || parameter.Type != "BIGINT" || parameter.Value == nil || *parameter.Value != value {
			t.Fatalf("parameter[%d] = %#v", index, parameter)
		}
	}
}

func TestTPCHAcceptanceExpectedResultIsFixedAndNonEmpty(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..", "..", "testdata", "acceptance", "databricks-tpch")
	var expected model.TypedResult
	read(t, filepath.Join(root, "expected.json"), &expected)

	wantColumns := []model.ResultColumn{
		{Name: "customer_segment", TypeName: "STRING", TypeText: "STRING"},
		{Name: "order_status", TypeName: "STRING", TypeText: "STRING"},
		{Name: "gross_revenue", TypeName: "DECIMAL", TypeText: "DECIMAL(28,2)"},
		{Name: "order_count", TypeName: "LONG", TypeText: "BIGINT"},
		{Name: "average_order_value", TypeName: "DECIMAL", TypeText: "DECIMAL(38,12)"},
	}
	gotColumns, err := json.Marshal(expected.Columns)
	if err != nil {
		t.Fatal(err)
	}
	expectedColumns, err := json.Marshal(wantColumns)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotColumns) != string(expectedColumns) {
		t.Fatalf("expected columns = %s, want %s", gotColumns, expectedColumns)
	}
	if expected.Truncated || len(expected.Rows) != 5 {
		t.Fatalf("expected result = %#v", expected)
	}
	for rowIndex, row := range expected.Rows {
		if len(row) != len(expected.Columns) {
			t.Fatalf("expected row %d has %d values for %d columns", rowIndex, len(row), len(expected.Columns))
		}
	}
}
