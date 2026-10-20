package postgresquery

import (
	"github.com/marmot1024/metricspire/internal/adapter/onlinecontract"
	"github.com/marmot1024/metricspire/internal/model"
)

// DataContractFingerprint retains the existing PostgreSQL producer contract.
func DataContractFingerprint(manifest model.SemanticManifest, binding model.SourceBinding) (string, error) {
	return onlinecontract.Fingerprint(manifest, binding, EngineName, "postgres-online-data-v1")
}
