package main

import (
	"path/filepath"
	"testing"

	"github.com/marmot1024/metricspire/internal/adapter/databricks"
	"github.com/marmot1024/metricspire/internal/adapter/postgresquery"
	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/runtimeconfig"
)

func TestAnalyticalAndOnlineBindingsRemainSeparate(t *testing.T) {
	base := t.TempDir()
	var binding model.SourceBinding
	if err := contractio.ReadFile(filepath.Join("..", "..", "examples", "online", "binding.yaml"), &binding); err != nil {
		t.Fatal(err)
	}
	if err := contractio.WriteJSON(filepath.Join(base, "online.json"), binding); err != nil {
		t.Fatal(err)
	}
	online := runtimeconfig.OnlineConfig{Bindings: []runtimeconfig.BindingRoute{{Namespace: "demo", ModelName: "daily_sales", Path: "online.json"}}, Resources: []model.ResourceRef{binding.Datasets[0].Resource}}
	loadedOnline, err := loadOnlineBindings(base, online)
	if err != nil {
		t.Fatal(err)
	}
	binding.Engine = databricks.EngineName
	if err := contractio.WriteJSON(filepath.Join(base, "warehouse.json"), binding); err != nil {
		t.Fatal(err)
	}
	_, analytical, err := loadTrustedRoutes(base, runtimeconfig.Config{Bindings: []runtimeconfig.BindingRoute{{Namespace: "demo", ModelName: "daily_sales", Path: "warehouse.json"}}, Online: &online})
	if err != nil {
		t.Fatal(err)
	}
	if loadedOnline[0].Binding.Engine != postgresquery.EngineName || analytical[0].Binding.Engine != databricks.EngineName {
		t.Fatal("routes contaminated each other")
	}
	for _, test := range []struct {
		name   string
		change func(*model.SourceBinding)
	}{
		{"warehouse", func(b *model.SourceBinding) { b.Engine = databricks.EngineName }},
		{"unapproved source", func(b *model.SourceBinding) { b.Datasets[0].Resource.Table = "unapproved" }},
		{"multiple sources", func(b *model.SourceBinding) { b.Datasets = append(b.Datasets, b.Datasets[0]) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var b model.SourceBinding
			if err := contractio.ReadFile(filepath.Join("..", "..", "examples", "online", "binding.yaml"), &b); err != nil {
				t.Fatal(err)
			}
			test.change(&b)
			if err := contractio.WriteJSON(filepath.Join(base, "online.json"), b); err != nil {
				t.Fatal(err)
			}
			if _, err := loadOnlineBindings(base, online); err == nil {
				t.Fatal("unsafe online binding accepted")
			}
		})
	}
}
