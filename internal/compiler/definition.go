package compiler

import (
	"strings"

	"github.com/marmot1024/metricspire/internal/model"
)

func validateDefinition(d *model.MetricDefinition, path string) error {
	if d == nil {
		return nil
	}
	if len(d.Family) > 128 || len(d.Variant) > 128 || !namePattern.MatchString(d.Family) || !namePattern.MatchString(d.Variant) {
		return problem("invalid_definition", path, "family and variant must be bounded semantic identifiers")
	}
	for _, text := range []string{d.Population, d.Numerator, d.Denominator, d.CountingUnit, d.TimeBasis} {
		if strings.TrimSpace(text) == "" || len(text) > 1024 {
			return problem("invalid_definition", path, "population, numerator, denominator, counting_unit and time_basis must be nonempty and at most 1024 bytes; use explicit not-applicable descriptions where appropriate")
		}
	}
	if d.Exclusions == nil || len(d.Exclusions) > 20 {
		return problem("invalid_definition", path+".exclusions", "declare exclusions explicitly; use [] for no exclusions, at most 20 entries")
	}
	for _, text := range d.Exclusions {
		if strings.TrimSpace(text) == "" || len(text) > 512 {
			return problem("invalid_definition", path+".exclusions", "exclusions must be nonempty and at most 512 bytes")
		}
	}
	return rejectDuplicates(d.Exclusions, path+".exclusions")
}
