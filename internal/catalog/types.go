// Package catalog refreshes and stores Terraform provider/module metadata and
// generates cross-provider mapping candidates.
package catalog

import (
	"encoding/json"
	"time"
)

const FormatVersion = "1.0"

// ProviderSpec identifies one provider to refresh.
type ProviderSpec struct {
	Name    string `json:"name"`
	Source  string `json:"source"`
	Version string `json:"version"`
}

// Config controls one immutable catalog snapshot refresh.
type Config struct {
	OutputDir       string
	TerraformBinary string
	RegistryBaseURL string
	Providers       []ProviderSpec
	RefreshModules  bool
	ModuleLimit     int
	ModuleDetails   bool
	DetailWorkers   int
	DetailRPS       int
	RequestTimeout  time.Duration
	CommandTimeout  time.Duration
	OverridesPath   string
	Progress        func(format string, args ...interface{})
}

// LatestPointer atomically identifies the current completed snapshot.
type LatestPointer struct {
	FormatVersion string    `json:"format_version"`
	UpdatedAt     time.Time `json:"updated_at"`
	Manifest      string    `json:"manifest"`
}

// Manifest describes a complete refresh snapshot.
type Manifest struct {
	FormatVersion    string                      `json:"format_version"`
	SnapshotID       string                      `json:"snapshot_id"`
	RefreshedAt      time.Time                   `json:"refreshed_at"`
	TerraformVersion string                      `json:"terraform_version"`
	Providers        map[string]ProviderArtifact `json:"providers"`
	Modules          map[string]ModuleArtifact   `json:"modules,omitempty"`
	Mappings         map[string]string           `json:"mappings"`
}

type ProviderArtifact struct {
	Source              string `json:"source"`
	Version             string `json:"version"`
	SchemaFormatVersion string `json:"schema_format_version"`
	RawSchemaFile       string `json:"raw_schema_file"`
	IndexFile           string `json:"index_file"`
	Resources           int    `json:"resources"`
	DataSources         int    `json:"data_sources"`
	EphemeralResources  int    `json:"ephemeral_resources"`
	Functions           int    `json:"functions"`
}

type ModuleArtifact struct {
	File          string `json:"file"`
	Modules       int    `json:"modules"`
	Detailed      int    `json:"detailed"`
	RegistryQuery string `json:"registry_query"`
}

// ProviderIndex is a compact, searchable representation of a raw provider schema.
type ProviderIndex struct {
	FormatVersion       string                 `json:"format_version"`
	RefreshedAt         time.Time              `json:"refreshed_at"`
	Name                string                 `json:"name"`
	Source              string                 `json:"source"`
	Version             string                 `json:"version"`
	SchemaFormatVersion string                 `json:"schema_format_version"`
	ProviderConfig      ObjectIndex            `json:"provider_config"`
	Resources           map[string]ObjectIndex `json:"resources"`
	DataSources         map[string]ObjectIndex `json:"data_sources"`
	EphemeralResources  map[string]ObjectIndex `json:"ephemeral_resources,omitempty"`
	Functions           map[string]Function    `json:"functions,omitempty"`
}

type ObjectIndex struct {
	SchemaVersion int64            `json:"schema_version"`
	Description   string           `json:"description,omitempty"`
	Attributes    []AttributeIndex `json:"attributes"`
	Blocks        []BlockIndex     `json:"blocks,omitempty"`
}

type AttributeIndex struct {
	Path        string          `json:"path"`
	Type        json.RawMessage `json:"type,omitempty"`
	Description string          `json:"description,omitempty"`
	Required    bool            `json:"required,omitempty"`
	Optional    bool            `json:"optional,omitempty"`
	Computed    bool            `json:"computed,omitempty"`
	Sensitive   bool            `json:"sensitive,omitempty"`
	Deprecated  bool            `json:"deprecated,omitempty"`
}

type BlockIndex struct {
	Path        string `json:"path"`
	NestingMode string `json:"nesting_mode"`
	MinItems    int    `json:"min_items,omitempty"`
	MaxItems    int    `json:"max_items,omitempty"`
}

type Function struct {
	Summary            string              `json:"summary,omitempty"`
	Description        string              `json:"description,omitempty"`
	DeprecationMessage string              `json:"deprecation_message,omitempty"`
	ReturnType         json.RawMessage     `json:"return_type,omitempty"`
	Parameters         []FunctionParameter `json:"parameters,omitempty"`
	VariadicParameter  *FunctionParameter  `json:"variadic_parameter,omitempty"`
}

type FunctionParameter struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	IsNullable  bool            `json:"is_nullable,omitempty"`
	Type        json.RawMessage `json:"type,omitempty"`
}

// ModuleCatalog stores all summary fields returned by the documented Registry
// listing API. Raw preserves future fields without requiring a format change.
type ModuleCatalog struct {
	FormatVersion string         `json:"format_version"`
	RefreshedAt   time.Time      `json:"refreshed_at"`
	Provider      string         `json:"provider"`
	Modules       []ModuleRecord `json:"modules"`
	Details       []ModuleDetail `json:"details,omitempty"`
}

type ModuleRecord struct {
	ID          string          `json:"id"`
	Namespace   string          `json:"namespace"`
	Name        string          `json:"name"`
	Version     string          `json:"version"`
	Provider    string          `json:"provider"`
	Description string          `json:"description,omitempty"`
	Source      string          `json:"source,omitempty"`
	PublishedAt string          `json:"published_at,omitempty"`
	Downloads   int64           `json:"downloads,omitempty"`
	Verified    bool            `json:"verified,omitempty"`
	Raw         json.RawMessage `json:"raw"`
}

type ModuleDetail struct {
	ID  string          `json:"id"`
	Raw json.RawMessage `json:"raw"`
}

// MappingCatalog contains ranked, deterministic candidates. Manual indicates
// an explicit override; generated candidates must still be reviewed.
type MappingCatalog struct {
	FormatVersion  string                     `json:"format_version"`
	GeneratedAt    time.Time                  `json:"generated_at"`
	SourceProvider string                     `json:"source_provider"`
	TargetProvider string                     `json:"target_provider"`
	Resources      []ResourceMappingCandidate `json:"resources"`
	DataSources    []ResourceMappingCandidate `json:"data_sources"`
	Modules        []ModuleMappingCandidate   `json:"modules,omitempty"`
}

type ResourceMappingCandidate struct {
	SourceType string                      `json:"source_type"`
	TargetType string                      `json:"target_type"`
	Score      float64                     `json:"score"`
	Confidence string                      `json:"confidence"`
	Manual     bool                        `json:"manual,omitempty"`
	Reasons    []string                    `json:"reasons,omitempty"`
	Attributes []AttributeMappingCandidate `json:"attributes,omitempty"`
	Alternates []TypeCandidate             `json:"alternates,omitempty"`
}

type TypeCandidate struct {
	TargetType string   `json:"target_type"`
	Score      float64  `json:"score"`
	Confidence string   `json:"confidence"`
	Reasons    []string `json:"reasons,omitempty"`
}

type AttributeMappingCandidate struct {
	SourcePath string   `json:"source_path"`
	TargetPath string   `json:"target_path"`
	Score      float64  `json:"score"`
	Confidence string   `json:"confidence"`
	Manual     bool     `json:"manual,omitempty"`
	Reasons    []string `json:"reasons,omitempty"`
}

type ModuleMappingCandidate struct {
	SourceID string  `json:"source_id"`
	TargetID string  `json:"target_id"`
	Score    float64 `json:"score"`
}

type OverrideFile struct {
	Resources []ResourceOverride `json:"resources"`
}

type ResourceOverride struct {
	SourceProvider string            `json:"source_provider"`
	TargetProvider string            `json:"target_provider"`
	SourceType     string            `json:"source_type"`
	TargetType     string            `json:"target_type"`
	Attributes     map[string]string `json:"attributes,omitempty"`
}
