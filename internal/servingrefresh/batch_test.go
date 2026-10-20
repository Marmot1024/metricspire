package servingrefresh

import (
	"bytes"
	"github.com/marmot1024/metricspire/internal/model"
	"strings"
	"testing"
	"time"
)

func pointer(s string) *string { return &s }
func testContract() Contract {
	return Contract{Fingerprint: "sha256:synthetic", Resource: model.ResourceRef{Kind: model.ResourceTable, Schema: "serving", Table: "daily_sales"}, Key: "id", Columns: []Column{{Name: "id", Type: model.DataTypeString}, {Name: "value", Type: model.DataTypeDecimal, Nullable: true}}}
}
func artifact(t *testing.T, c Contract, batch string, asof time.Time, values string, count int64, total *string) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := Export(strings.NewReader(values), &b, c, Header{Tenant: "demo", BatchID: batch, DataAsOf: asof}, Expectations{Count: count, Totals: map[string]*string{"value": total}}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestExactSourceReconciliationAndStrictRows(t *testing.T) {
	c := testContract()
	h := Header{Tenant: "demo", BatchID: "one", DataAsOf: time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)}
	valid := "{\"id\":\"a\",\"value\":\"9007199254740993.000001\"}\n{\"id\":\"b\",\"value\":\"-0.000001\"}\n"
	for _, tc := range []struct {
		rows  string
		count int64
		sum   *string
		good  bool
	}{
		{valid, 2, pointer("9007199254740993"), true}, {valid, 1, pointer("9007199254740993"), false}, {valid, 2, pointer("9007199254740992"), false},
		{"{\"id\":\"a\",\"value\":null}\n", 1, nil, true},
		{"{\"id\":\"a\",\"id\":\"b\",\"value\":\"1\"}\n", 1, pointer("1"), false},
		{"{\"id\":\"a\",\"value\":1}\n", 1, pointer("1"), false},
		{"{\"id\":null,\"value\":\"1\"}\n", 1, pointer("1"), false},
		{"{\"id\":\"a\",\"value\":\"1e2\"}\n", 1, pointer("100"), false},
		{"{\"id\":\"a\",\"value\":\"1\",\"sql\":\"no\"}\n", 1, pointer("1"), false},
	} {
		var out bytes.Buffer
		err := Export(strings.NewReader(tc.rows), &out, c, h, Expectations{Count: tc.count, Totals: map[string]*string{"value": tc.sum}})
		if (err == nil) != tc.good {
			t.Fatalf("good=%v error=%v", tc.good, err)
		}
	}
}

func TestExportRejectsSubMicrosecondWatermark(t *testing.T) {
	var out bytes.Buffer
	err := Export(strings.NewReader("{\"id\":\"a\",\"value\":\"1\"}\n"), &out, testContract(), Header{Tenant: "demo", BatchID: "a", DataAsOf: time.Date(2026, 9, 1, 0, 0, 0, 123456789, time.UTC)}, Expectations{Count: 1, Totals: map[string]*string{"value": pointer("1")}})
	if err == nil || out.Len() != 0 {
		t.Fatal("nanosecond watermark exported")
	}
}
