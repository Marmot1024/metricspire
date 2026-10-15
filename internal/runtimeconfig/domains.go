package runtimeconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/exploration"
)

// Domains are deployment-owned business names, not authorization grants.
// Source provenance and paths must never be returned in public discovery.
type DomainRegistry struct {
	Catalogs   []exploration.Catalog `json:"-" yaml:"-"`
	APIVersion string                `json:"api_version" yaml:"api_version"`
	Kind       string                `json:"kind" yaml:"kind"`
	Domains    []BusinessDomain      `json:"domains" yaml:"domains"`
}

type BusinessDomain struct {
	Tenant         string             `json:"tenant" yaml:"tenant"`
	Namespace      string             `json:"namespace" yaml:"namespace"`
	DisplayName    string             `json:"display_name" yaml:"display_name"`
	Aliases        []string           `json:"aliases,omitempty" yaml:"aliases,omitempty"`
	SemanticSource *SemanticSourceRef `json:"semantic_source,omitempty" yaml:"semantic_source,omitempty"`
}

// The operator exports reviewed catalogs from this revision. Runtime verifies
// the exported bytes, not arbitrary repository YAML. It never fetches Git or
// executes code from a semantic repository during a query or startup.
type SemanticSourceRef struct {
	Repository string            `json:"repository" yaml:"repository"`
	Revision   string            `json:"revision" yaml:"revision"`
	Catalogs   []CatalogArtifact `json:"catalogs,omitempty" yaml:"catalogs,omitempty"`
}

type CatalogArtifact struct {
	Path   string `json:"path" yaml:"path"`
	SHA256 string `json:"sha256" yaml:"sha256"`
}

func LoadDomains(path string) (DomainRegistry, error) {
	var r DomainRegistry
	if err := readDomainRegistry(path, &r); err != nil {
		return r, err
	}
	if err := r.Validate(); err != nil {
		return r, err
	}
	base, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return r, err
	}
	routes := map[string]bool{}
	for i := range r.Domains {
		s := r.Domains[i].SemanticSource
		if s == nil {
			continue
		}
		for j := range s.Catalogs {
			p := s.Catalogs[j].Path
			if !filepath.IsAbs(p) {
				p = filepath.Join(base, p)
			}
			// Keep artifact reads bounded and decode exactly the verified bytes.
			f, err := os.Open(p)
			if err != nil {
				return r, errors.New("cannot read configured semantic catalog artifact")
			}
			info, err := f.Stat()
			if err != nil || !info.Mode().IsRegular() || info.Size() > 8<<20 {
				f.Close()
				return r, errors.New("semantic catalog artifact must be a regular file of at most 8 MiB")
			}
			data, err := io.ReadAll(io.LimitReader(f, (8<<20)+1))
			f.Close()
			if err != nil || len(data) > 8<<20 {
				return r, errors.New("cannot read configured semantic catalog artifact")
			}
			sum := sha256.Sum256(data)
			if hex.EncodeToString(sum[:]) != s.Catalogs[j].SHA256 {
				return r, errors.New("semantic catalog artifact checksum mismatch")
			}
			var catalog exploration.Catalog
			switch strings.ToLower(filepath.Ext(p)) {
			case ".json":
				err = contractio.DecodeJSON(data, &catalog)
			case ".yaml", ".yml":
				err = contractio.DecodeYAML(data, &catalog)
			default:
				err = errors.New("unsupported catalog extension")
			}
			if err != nil {
				return r, errors.New("invalid configured analysis catalog")
			}
			if catalog.Namespace != r.Domains[i].Namespace || catalog.Tenant != r.Domains[i].Tenant {
				return r, errors.New("semantic catalog tenant or namespace differs from business domain")
			}
			if err := exploration.ValidateCatalog(catalog); err != nil {
				return r, errors.New("configured analysis catalog failed validation")
			}
			for _, source := range catalog.Sources {
				key := catalog.Tenant + "\x00" + catalog.Namespace + "\x00" + source.Name
				if routes[key] {
					return r, errors.New("ambiguous analysis source across domain artifacts")
				}
				routes[key] = true
			}
			r.Catalogs = append(r.Catalogs, catalog)
			s.Catalogs[j].Path = p
		}
	}
	return r, nil
}

func (r DomainRegistry) Validate() error {
	if r.APIVersion != APIVersion || r.Kind != "DomainRegistry" || len(r.Domains) == 0 || len(r.Domains) > 64 {
		return errors.New("domain registry requires api_version, kind DomainRegistry and 1-64 domains")
	}
	seen := map[string]string{}
	namespaces := map[string]bool{}
	artifacts := 0
	paths := map[string]bool{}
	for _, d := range r.Domains {
		if strings.TrimSpace(d.Tenant) == "" || strings.TrimSpace(d.Tenant) != d.Tenant || len(d.Tenant) > 128 || !regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`).MatchString(d.Namespace) || strings.TrimSpace(d.DisplayName) == "" || len(d.DisplayName) > 256 || len(d.Aliases) > 20 {
			return errors.New("business domain requires bounded tenant, namespace, display_name and aliases")
		}
		key := d.Tenant + "\x00" + d.Namespace
		if namespaces[key] {
			return errors.New("duplicate business domain namespace within tenant")
		}
		namespaces[key] = true
		for _, name := range append([]string{d.Namespace, d.DisplayName}, d.Aliases...) {
			if strings.TrimSpace(name) == "" || len(name) > 256 {
				return errors.New("business aliases must be nonempty and bounded")
			}
			alias := d.Tenant + "\x00" + strings.ToLower(strings.Join(strings.Fields(name), " "))
			if previous, ok := seen[alias]; ok && previous != d.Namespace {
				return errors.New("ambiguous business alias within tenant")
			}
			seen[alias] = d.Namespace
		}
		if s := d.SemanticSource; s != nil {
			artifacts += len(s.Catalogs)
			if artifacts > 32 {
				return errors.New("domain registry supports at most 32 catalog artifacts")
			}
			u, err := url.Parse(s.Repository)
			if err != nil || u.Scheme != "https" || u.Host == "" || u.Path == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(s.Revision) || len(s.Catalogs) > 32 {
				return errors.New("semantic source requires an HTTPS repository without credentials and a full immutable commit revision")
			}
			for _, a := range s.Catalogs {
				key := filepath.Clean(a.Path)
				if paths[key] {
					return errors.New("duplicate semantic catalog artifact path")
				}
				paths[key] = true
				if strings.TrimSpace(a.Path) == "" || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(a.SHA256) {
					return errors.New("semantic catalog requires path and SHA-256")
				}
			}
		}
	}
	return nil
}

func (c *Config) loadDomainRegistry(configPath string) error {
	if c.DomainsPath == "" {
		return nil
	}
	p := c.DomainsPath
	if !filepath.IsAbs(p) {
		p = filepath.Join(filepath.Dir(configPath), p)
	}
	r, err := LoadDomains(p)
	if err != nil {
		return err
	}
	c.Domains = r.Domains
	if len(r.Catalogs) > 0 {
		if c.Exploration == nil {
			return errors.New("domain catalogs require explicit exploration configuration")
		}
		c.Exploration.ReviewedCatalogs = append(c.Exploration.ReviewedCatalogs, r.Catalogs...)
	}
	return nil
}

// Decode the bounded registry without network access or secret expansion.
func readDomainRegistry(path string, r *DomainRegistry) error {
	f, err := os.Open(path)
	if err != nil {
		return errors.New("cannot open domain registry")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("domain registry must be a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return errors.New("domain registry exceeds 1 MiB or cannot be read")
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		err = contractio.DecodeJSON(data, r)
	case ".yaml", ".yml":
		err = contractio.DecodeYAML(data, r)
	default:
		return errors.New("domain registry must be JSON or YAML")
	}
	if err != nil {
		return errors.New("invalid domain registry: check schema, duplicate keys and unknown fields")
	}
	return nil
}
