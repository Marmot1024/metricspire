// Package canonical provides deterministic JSON and content fingerprints.
package canonical

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

func Marshal(value any) ([]byte, error) {
	return json.Marshal(value)
}

func MarshalIndent(value any) ([]byte, error) {
	return json.MarshalIndent(value, "", "  ")
}

func Fingerprint(value any) (string, error) {
	data, err := Marshal(value)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(hash[:]), nil
}
