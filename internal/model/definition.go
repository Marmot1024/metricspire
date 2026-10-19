package model

// MetricDefinition describes reviewed business meaning, not physical SQL.
// The containing immutable manifest versions the definition and its formula.
type MetricDefinition struct {
	Family       string   `json:"family" yaml:"family"`
	Variant      string   `json:"variant" yaml:"variant"`
	Population   string   `json:"population" yaml:"population"`
	Exclusions   []string `json:"exclusions" yaml:"exclusions"`
	Numerator    string   `json:"numerator" yaml:"numerator"`
	Denominator  string   `json:"denominator" yaml:"denominator"`
	CountingUnit string   `json:"counting_unit" yaml:"counting_unit"`
	TimeBasis    string   `json:"time_basis" yaml:"time_basis"`
}
