// Package servingrefresh validates operator-produced DWS/ADS snapshot artifacts.
// It never extracts from a warehouse or accepts SQL from a business query.
package servingrefresh

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/marmot1024/metricspire/internal/adapter/postgresquery"
	"github.com/marmot1024/metricspire/internal/model"
)

const Version = "metricspire.io/online-batch-v1"

var identifier = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
var batchID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
var decimal = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)

type Column struct {
	Name     string         `json:"name"`
	Type     model.DataType `json:"type"`
	Nullable bool           `json:"nullable"`
}
type Contract struct {
	Fingerprint string
	Resource    model.ResourceRef
	Columns     []Column
	Key         string
}
type Header struct {
	APIVersion string             `json:"api_version"`
	Kind       string             `json:"kind"`
	Contract   string             `json:"data_contract"`
	Tenant     string             `json:"tenant"`
	BatchID    string             `json:"batch_id"`
	DataAsOf   time.Time          `json:"data_as_of"`
	RowCount   int64              `json:"row_count"`
	SHA256     string             `json:"sha256"`
	Columns    []Column           `json:"columns"`
	Totals     map[string]*string `json:"totals"`
}

// Totals come independently from the upstream job, rather than from the artifact
// being checked. NULL means the source aggregate had no non-null values.
type Expectations struct {
	Count  int64              `json:"count"`
	Totals map[string]*string `json:"totals"`
}

func NewContract(m model.SemanticManifest, b model.SourceBinding) (Contract, error) {
	fp, err := postgresquery.DataContractFingerprint(m, b)
	if err != nil {
		return Contract{}, err
	}
	if b.ManifestFingerprint != "" && b.ManifestFingerprint != m.Fingerprint {
		return Contract{}, errors.New("refresh binding manifest fingerprint mismatch")
	}
	if len(m.Definitions.Datasets) != 1 || len(m.Definitions.Entities) != 1 || len(b.Datasets) != 1 {
		return Contract{}, errors.New("refresh requires one reviewed dataset and entity")
	}
	if b.Datasets[0].Name != m.Definitions.Datasets[0].Name || m.Definitions.Entities[0].Dataset != m.Definitions.Datasets[0].Name {
		return Contract{}, errors.New("refresh dataset and entity binding must match")
	}
	r := b.Datasets[0].Resource
	if r.Kind != model.ResourceTable || r.Catalog != "" || r.URI != "" || !identifier.MatchString(r.Schema) || !identifier.MatchString(r.Table) || r.Schema == "public" || r.Schema == "metricspire" || strings.HasPrefix(r.Schema, "pg_") || r.Schema == "information_schema" || (strings.HasPrefix(r.Table, "metricspire_") || strings.HasPrefix(r.Table, "_metricspire_")) {
		return Contract{}, errors.New("refresh target must be an explicit non-control serving table")
	}
	c := Contract{Fingerprint: fp, Resource: r}
	fields := map[string]model.DataType{}
	nullable := map[string]bool{}
	for _, f := range m.Definitions.Datasets[0].Fields {
		fields[f.Name] = f.DataType
		nullable[f.Name] = f.Nullable
	}
	seen := map[string]bool{}
	logicalSeen := map[string]bool{}
	for _, f := range b.Datasets[0].Fields {
		typ, ok := fields[f.Name]
		if !ok || !identifier.MatchString(f.Column) || strings.HasPrefix(f.Column, "_metricspire_") || seen[f.Column] || logicalSeen[f.Name] {
			return Contract{}, errors.New("refresh requires a unique explicit binding for every field")
		}
		switch typ {
		case model.DataTypeString, model.DataTypeDate, model.DataTypeBoolean, model.DataTypeInteger, model.DataTypeDecimal:
		default:
			return Contract{}, errors.New("unsupported refresh field type")
		}
		seen[f.Column] = true
		logicalSeen[f.Name] = true
		c.Columns = append(c.Columns, Column{Name: f.Column, Type: typ, Nullable: nullable[f.Name]})
		if f.Name == m.Definitions.Entities[0].Key {
			c.Key = f.Column
			if typ != model.DataTypeString && typ != model.DataTypeInteger {
				return Contract{}, errors.New("refresh entity key must be string or integer")
			}
		}
	}
	if len(c.Columns) != len(fields) || c.Key == "" {
		return Contract{}, errors.New("refresh binding must cover every field and the entity key")
	}
	sort.Slice(c.Columns, func(i, j int) bool { return c.Columns[i].Name < c.Columns[j].Name })
	return c, nil
}
func numeric(t model.DataType) bool { return t == model.DataTypeDecimal || t == model.DataTypeInteger }
func scalar(v *string, t model.DataType) error {
	if v == nil {
		return nil
	}
	switch t {
	case model.DataTypeString:
		if len(*v) > 4096 {
			return errors.New("oversized refresh string")
		}
	case model.DataTypeInteger:
		if _, err := strconv.ParseInt(*v, 10, 64); err != nil {
			return errors.New("invalid refresh integer")
		}
	case model.DataTypeDecimal:
		if !decimal.MatchString(*v) || len(strings.ReplaceAll(strings.TrimPrefix(*v, "-"), ".", "")) > 38 {
			return errors.New("invalid refresh decimal")
		}
		if parts := strings.Split(*v, "."); len(parts) == 2 && len(parts[1]) > 18 {
			return errors.New("refresh decimal scale exceeds 18")
		}
	case model.DataTypeDate:
		if _, err := time.Parse(time.DateOnly, *v); err != nil {
			return errors.New("invalid refresh date")
		}
	case model.DataTypeBoolean:
		if *v != "true" && *v != "false" {
			return errors.New("invalid refresh boolean")
		}
	default:
		return errors.New("unsupported refresh scalar")
	}
	return nil
}

type rows struct {
	s      *bufio.Scanner
	c      Contract
	count  int64
	digest hash.Hash
	sums   map[string]*big.Rat
	last   map[string]*string
	err    error
}

func newRows(r io.Reader, c Contract) *rows {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 65536)
	return &rows{s: scanner, c: c, digest: sha256.New(), sums: map[string]*big.Rat{}}
}
func (r *rows) next() bool {
	if r.err != nil || !r.s.Scan() {
		if r.err == nil {
			r.err = r.s.Err()
		}
		return false
	}
	if r.count >= 50000000 {
		r.err = errors.New("refresh exceeds fifty million rows")
		return false
	}
	v := map[string]*string{}
	if !utf8.Valid(r.s.Bytes()) {
		r.err = errors.New("invalid refresh UTF-8")
		return false
	}
	dec := json.NewDecoder(strings.NewReader(r.s.Text()))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		r.err = errors.New("invalid refresh row")
		return false
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			r.err = err
			return false
		}
		name, ok := key.(string)
		if !ok {
			r.err = errors.New("invalid refresh field")
			return false
		}
		if _, exists := v[name]; exists {
			r.err = errors.New("duplicate refresh field")
			return false
		}
		var value *string
		if err = dec.Decode(&value); err != nil {
			r.err = err
			return false
		}
		v[name] = value
	}
	if _, err = dec.Token(); err != nil {
		r.err = err
		return false
	}
	var extra any
	if len(v) != len(r.c.Columns) || dec.Decode(&extra) != io.EOF {
		r.err = errors.New("invalid refresh row shape")
		return false
	}
	for _, col := range r.c.Columns {
		value, ok := v[col.Name]
		if !ok || scalar(value, col.Type) != nil || (value == nil && (col.Name == r.c.Key || !col.Nullable)) {
			r.err = errors.New("refresh row has missing or invalid typed values")
			return false
		}
		if numeric(col.Type) && value != nil {
			n, _ := new(big.Rat).SetString(*value)
			if r.sums[col.Name] == nil {
				r.sums[col.Name] = new(big.Rat)
			}
			r.sums[col.Name].Add(r.sums[col.Name], n)
		}
	}
	encoded, _ := json.Marshal(v)
	r.digest.Write(encoded)
	r.digest.Write([]byte{'\n'})
	r.last = v
	r.count++
	return true
}
func (r *rows) check(count int64, totals map[string]*string) error {
	if r.err != nil {
		return errors.New("refresh artifact read or typed validation failed")
	}
	if count < 1 || r.count != count {
		return errors.New("refresh source count reconciliation failed")
	}
	expectedFields := 0
	for _, col := range r.c.Columns {
		if numeric(col.Type) {
			expectedFields++
			expected, ok := totals[col.Name]
			if !ok {
				return errors.New("upstream totals required for every numeric column")
			}
			actual := r.sums[col.Name]
			if expected == nil {
				if actual != nil {
					return errors.New("refresh NULL total mismatch")
				}
				continue
			}
			if len(*expected) > 128 || !decimal.MatchString(*expected) {
				return errors.New("invalid upstream total")
			}
			want, ok := new(big.Rat).SetString(*expected)
			if !ok || actual == nil || actual.Cmp(want) != 0 {
				return errors.New("refresh source sum reconciliation failed")
			}
		}
	}
	if len(totals) != expectedFields {
		return errors.New("unexpected upstream total columns")
	}
	return nil
}
func Export(input io.ReadSeeker, out io.Writer, c Contract, h Header, e Expectations) error {
	if h.Tenant == "" || len(h.Tenant) > 128 || !batchID.MatchString(h.BatchID) || h.DataAsOf.Nanosecond()%1000 != 0 || h.DataAsOf.IsZero() || h.DataAsOf.Location() != time.UTC || h.DataAsOf.After(time.Now().Add(time.Second)) {
		return errors.New("tenant, batch ID and explicit UTC source watermark required")
	}
	r := newRows(input, c)
	for r.next() {
	}
	if err := r.check(e.Count, e.Totals); err != nil {
		return err
	}
	h.APIVersion = Version
	h.Kind = "OnlineBatch"
	h.Contract = c.Fingerprint
	h.Columns = c.Columns
	h.RowCount = r.count
	h.SHA256 = hex.EncodeToString(r.digest.Sum(nil))
	h.Totals = e.Totals
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := json.NewEncoder(out).Encode(h); err != nil {
		return err
	}
	again := newRows(input, c)
	for again.next() {
		if err := json.NewEncoder(out).Encode(again.last); err != nil {
			return err
		}
	}
	if err := again.check(e.Count, e.Totals); err != nil {
		return err
	}
	if hex.EncodeToString(again.digest.Sum(nil)) != h.SHA256 {
		return errors.New("source changed during export")
	}
	return nil
}
func readHeader(r *bufio.Reader, c Contract) (Header, error) {
	var h Header
	line, err := r.ReadSlice('\n')
	if err != nil || len(line) > 65536 {
		return h, errors.New("invalid online batch header")
	}
	dec := json.NewDecoder(strings.NewReader(string(line)))
	dec.DisallowUnknownFields()
	var extra any
	if dec.Decode(&h) != nil || dec.Decode(&extra) != io.EOF {
		return h, errors.New("invalid online batch header")
	}
	_, hexErr := hex.DecodeString(h.SHA256)
	if hexErr != nil || h.DataAsOf.Location() != time.UTC || h.APIVersion != Version || h.Kind != "OnlineBatch" || h.Contract != c.Fingerprint || h.Tenant == "" || len(h.Tenant) > 128 || !batchID.MatchString(h.BatchID) || h.RowCount < 1 || h.RowCount > 50000000 || len(h.SHA256) != 64 || h.DataAsOf.Nanosecond()%1000 != 0 || h.DataAsOf.IsZero() || h.DataAsOf.After(time.Now().Add(time.Second)) {
		return h, errors.New("online batch contract or metadata mismatch")
	}
	want, _ := json.Marshal(c.Columns)
	got, _ := json.Marshal(h.Columns)
	if string(want) != string(got) {
		return h, errors.New("online batch columns do not match reviewed contract")
	}
	return h, nil
}
func (r *rows) verify(h Header) error {
	if err := r.check(h.RowCount, h.Totals); err != nil {
		return err
	}
	if hex.EncodeToString(r.digest.Sum(nil)) != h.SHA256 {
		return errors.New("online batch checksum mismatch")
	}
	return nil
}

func decimalRat(s string) (*big.Rat, bool) { return new(big.Rat).SetString(s) }
