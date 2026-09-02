package compiler_test

import (
	"encoding/json"
	"errors"
	"math/rand/v2"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/marmot1024/metricspire/internal/compiler"
	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/model"
)

func TestCompileMatchesGolden(t *testing.T) {
	t.Parallel()
	source := loadSource(t)
	actual, err := compiler.Compile(source)
	if err != nil {
		t.Fatal(err)
	}
	var expected model.SemanticManifest
	read(t, filepath.Join("..", "..", "testdata", "golden", "manifest.json"), &expected)
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("compiled manifest differs from golden\nactual: %#v\nexpected: %#v", actual, expected)
	}
}

func TestCompileIsDeterministicAcrossSetOrdering(t *testing.T) {
	t.Parallel()
	source := loadSource(t)
	baseline, err := compiler.Compile(source)
	if err != nil {
		t.Fatal(err)
	}
	random := rand.New(rand.NewPCG(7, 11))
	for iteration := 0; iteration < 200; iteration++ {
		candidate := clone(t, source)
		random.Shuffle(len(candidate.Spec.Datasets), func(i, j int) {
			candidate.Spec.Datasets[i], candidate.Spec.Datasets[j] = candidate.Spec.Datasets[j], candidate.Spec.Datasets[i]
		})
		random.Shuffle(len(candidate.Spec.Entities), func(i, j int) {
			candidate.Spec.Entities[i], candidate.Spec.Entities[j] = candidate.Spec.Entities[j], candidate.Spec.Entities[i]
		})
		random.Shuffle(len(candidate.Spec.Dimensions), func(i, j int) {
			candidate.Spec.Dimensions[i], candidate.Spec.Dimensions[j] = candidate.Spec.Dimensions[j], candidate.Spec.Dimensions[i]
		})
		random.Shuffle(len(candidate.Spec.Relationships), func(i, j int) {
			candidate.Spec.Relationships[i], candidate.Spec.Relationships[j] = candidate.Spec.Relationships[j], candidate.Spec.Relationships[i]
		})
		random.Shuffle(len(candidate.Spec.Metrics), func(i, j int) {
			candidate.Spec.Metrics[i], candidate.Spec.Metrics[j] = candidate.Spec.Metrics[j], candidate.Spec.Metrics[i]
		})
		for i := range candidate.Spec.Datasets {
			random.Shuffle(len(candidate.Spec.Datasets[i].Fields), func(a, b int) {
				candidate.Spec.Datasets[i].Fields[a], candidate.Spec.Datasets[i].Fields[b] = candidate.Spec.Datasets[i].Fields[b], candidate.Spec.Datasets[i].Fields[a]
			})
		}
		for i := range candidate.Spec.Metrics {
			random.Shuffle(len(candidate.Spec.Metrics[i].AllowedDimensions), func(a, b int) {
				candidate.Spec.Metrics[i].AllowedDimensions[a], candidate.Spec.Metrics[i].AllowedDimensions[b] = candidate.Spec.Metrics[i].AllowedDimensions[b], candidate.Spec.Metrics[i].AllowedDimensions[a]
			})
			random.Shuffle(len(candidate.Spec.Metrics[i].Verification.Evidence), func(a, b int) {
				candidate.Spec.Metrics[i].Verification.Evidence[a], candidate.Spec.Metrics[i].Verification.Evidence[b] = candidate.Spec.Metrics[i].Verification.Evidence[b], candidate.Spec.Metrics[i].Verification.Evidence[a]
			})
		}
		actual, err := compiler.Compile(candidate)
		if err != nil {
			t.Fatalf("iteration %d: %v", iteration, err)
		}
		if actual.Fingerprint != baseline.Fingerprint {
			t.Fatalf("iteration %d fingerprint = %s, want %s", iteration, actual.Fingerprint, baseline.Fingerprint)
		}
	}
}

func TestCompileRejectsUnsafeCardinalityAndMetricCycles(t *testing.T) {
	t.Parallel()
	t.Run("cardinality", func(t *testing.T) {
		source := loadSource(t)
		source.Spec.Relationships[0].Cardinality = "one_to_many"
		_, err := compiler.Compile(source)
		assertProblem(t, err, "unsafe_cardinality")
	})
	t.Run("cycle", func(t *testing.T) {
		source := loadSource(t)
		for i := range source.Spec.Metrics {
			if source.Spec.Metrics[i].Name == "gross_revenue" {
				source.Spec.Metrics[i].Kind = model.MetricDerived
				source.Spec.Metrics[i].Expression = model.Expression{Op: model.OpMetric, Metric: "average_order_value"}
			}
		}
		_, err := compiler.Compile(source)
		assertProblem(t, err, "metric_cycle")
	})
}

func TestDerivedMetricCannotWidenDependencyDimensions(t *testing.T) {
	t.Parallel()
	source := loadSource(t)
	for i := range source.Spec.Metrics {
		if source.Spec.Metrics[i].Name == "gross_revenue" {
			source.Spec.Metrics[i].AllowedDimensions = []string{"order_date"}
		}
	}
	_, err := compiler.Compile(source)
	assertProblem(t, err, "invalid_metric")
}

func TestCompileRejectsDeclaredValueTypeThatDiffersFromExpression(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		metric    string
		valueType model.DataType
	}{
		{name: "integer sum declared decimal", metric: "order_count", valueType: model.DataTypeDecimal},
		{name: "division declared integer", metric: "refund_rate", valueType: model.DataTypeInteger},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := loadSource(t)
			for i := range source.Spec.Metrics {
				if source.Spec.Metrics[i].Name == test.metric {
					source.Spec.Metrics[i].ValueType = test.valueType
				}
			}
			_, err := compiler.Compile(source)
			assertProblem(t, err, "invalid_metric")
		})
	}
}

func TestCompilePolicyIsBoundAndDeterministic(t *testing.T) {
	t.Parallel()
	manifest, err := compiler.Compile(loadSource(t))
	if err != nil {
		t.Fatal(err)
	}
	var source model.PolicySource
	read(t, filepath.Join("..", "..", "examples", "orders", "policy.yaml"), &source)
	first, err := compiler.CompilePolicy(source, manifest)
	if err != nil {
		t.Fatal(err)
	}
	second, err := compiler.CompilePolicy(source, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint != second.Fingerprint || first.ManifestFingerprint != manifest.Fingerprint {
		t.Fatalf("policy was not deterministically bound: %#v %#v", first, second)
	}
	var expected model.PolicyBundle
	read(t, filepath.Join("..", "..", "testdata", "golden", "policy-bundle.json"), &expected)
	if !reflect.DeepEqual(first, expected) {
		t.Fatalf("compiled policy differs from golden")
	}
}

func TestCompiledArtifactsRejectMutation(t *testing.T) {
	t.Parallel()
	manifest, err := compiler.Compile(loadSource(t))
	if err != nil {
		t.Fatal(err)
	}
	manifest.Definitions.Metrics[0].Description = "tampered"
	assertProblem(t, compiler.VerifyManifest(manifest), "fingerprint_mismatch")
}

func FuzzCompileDeterministic(f *testing.F) {
	var source model.SemanticSource
	if err := contractio.ReadFile(filepath.Join("..", "..", "examples", "orders", "model.yaml"), &source); err != nil {
		f.Fatal(err)
	}
	seed, err := json.Marshal(source)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(seed)
	f.Fuzz(func(t *testing.T, data []byte) {
		var candidate model.SemanticSource
		if err := contractio.DecodeJSON(data, &candidate); err != nil {
			return
		}
		first, err := compiler.Compile(candidate)
		if err != nil {
			return
		}
		second, err := compiler.Compile(candidate)
		if err != nil || first.Fingerprint != second.Fingerprint {
			t.Fatalf("same input produced different result: %v %s %s", err, first.Fingerprint, second.Fingerprint)
		}
	})
}

func loadSource(t *testing.T) model.SemanticSource {
	t.Helper()
	var source model.SemanticSource
	read(t, filepath.Join("..", "..", "examples", "orders", "model.yaml"), &source)
	return source
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
		t.Fatalf("error = %v, want problem code %q", err, code)
	}
}
