package catalog

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

type schemaDocument struct {
	FormatVersion   string                     `json:"format_version"`
	ProviderSchemas map[string]json.RawMessage `json:"provider_schemas"`
}

type rawProviderSchema struct {
	Provider                 rawObject            `json:"provider"`
	ResourceSchemas          map[string]rawObject `json:"resource_schemas"`
	DataSourceSchemas        map[string]rawObject `json:"data_source_schemas"`
	EphemeralResourceSchemas map[string]rawObject `json:"ephemeral_resource_schemas"`
	Functions                map[string]Function  `json:"functions"`
}

type rawObject struct {
	Version int64    `json:"version"`
	Block   rawBlock `json:"block"`
}

type rawBlock struct {
	Attributes      map[string]rawAttribute `json:"attributes"`
	BlockTypes      map[string]rawBlockType `json:"block_types"`
	Description     string                  `json:"description"`
	DescriptionKind string                  `json:"description_kind"`
}

type rawAttribute struct {
	Type            json.RawMessage `json:"type"`
	NestedType      *rawNestedType  `json:"nested_type"`
	Description     string          `json:"description"`
	DescriptionKind string          `json:"description_kind"`
	Required        bool            `json:"required"`
	Optional        bool            `json:"optional"`
	Computed        bool            `json:"computed"`
	Sensitive       bool            `json:"sensitive"`
	Deprecated      bool            `json:"deprecated"`
}

type rawNestedType struct {
	Attributes  map[string]rawAttribute `json:"attributes"`
	NestingMode string                  `json:"nesting_mode"`
}

type rawBlockType struct {
	NestingMode string   `json:"nesting_mode"`
	MinItems    int      `json:"min_items"`
	MaxItems    int      `json:"max_items"`
	Block       rawBlock `json:"block"`
}

func splitProviderSchemas(data []byte, specs []ProviderSpec, refreshedAt time.Time) (map[string]json.RawMessage, map[string]*ProviderIndex, string, error) {
	var document schemaDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, nil, "", fmt.Errorf("decode terraform provider schemas: %w", err)
	}
	if !strings.HasPrefix(document.FormatVersion, "1.") {
		return nil, nil, "", fmt.Errorf("unsupported Terraform provider schema format %q", document.FormatVersion)
	}

	rawSchemas := make(map[string]json.RawMessage, len(specs))
	indexes := make(map[string]*ProviderIndex, len(specs))
	for _, spec := range specs {
		address, raw, ok := findProviderSchema(document.ProviderSchemas, spec.Source)
		if !ok {
			return nil, nil, "", fmt.Errorf("Terraform schema output does not contain provider %q", spec.Source)
		}
		var provider rawProviderSchema
		if err := json.Unmarshal(raw, &provider); err != nil {
			return nil, nil, "", fmt.Errorf("decode schema for %s: %w", spec.Name, err)
		}
		wrapped, err := json.MarshalIndent(schemaDocument{
			FormatVersion:   document.FormatVersion,
			ProviderSchemas: map[string]json.RawMessage{address: raw},
		}, "", "  ")
		if err != nil {
			return nil, nil, "", err
		}
		rawSchemas[spec.Name] = wrapped
		indexes[spec.Name] = buildProviderIndex(spec, document.FormatVersion, provider, refreshedAt)
	}
	return rawSchemas, indexes, document.FormatVersion, nil
}

func findProviderSchema(providers map[string]json.RawMessage, source string) (string, json.RawMessage, bool) {
	for address, schema := range providers {
		if address == source || strings.HasSuffix(address, "/"+source) || strings.HasSuffix(address, source) {
			return address, schema, true
		}
	}
	return "", nil, false
}

func buildProviderIndex(spec ProviderSpec, format string, provider rawProviderSchema, refreshedAt time.Time) *ProviderIndex {
	index := &ProviderIndex{
		FormatVersion:       FormatVersion,
		RefreshedAt:         refreshedAt,
		Name:                spec.Name,
		Source:              spec.Source,
		Version:             spec.Version,
		SchemaFormatVersion: format,
		ProviderConfig:      indexObject(provider.Provider),
		Resources:           indexObjects(provider.ResourceSchemas),
		DataSources:         indexObjects(provider.DataSourceSchemas),
		EphemeralResources:  indexObjects(provider.EphemeralResourceSchemas),
		Functions:           provider.Functions,
	}
	return index
}

func indexObjects(objects map[string]rawObject) map[string]ObjectIndex {
	indexed := make(map[string]ObjectIndex, len(objects))
	for name, object := range objects {
		indexed[name] = indexObject(object)
	}
	return indexed
}

func indexObject(object rawObject) ObjectIndex {
	indexed := ObjectIndex{
		SchemaVersion: object.Version,
		Description:   object.Block.Description,
	}
	flattenBlock("", object.Block, &indexed)
	sort.Slice(indexed.Attributes, func(i, j int) bool {
		return indexed.Attributes[i].Path < indexed.Attributes[j].Path
	})
	sort.Slice(indexed.Blocks, func(i, j int) bool {
		return indexed.Blocks[i].Path < indexed.Blocks[j].Path
	})
	return indexed
}

func flattenBlock(prefix string, block rawBlock, indexed *ObjectIndex) {
	for name, attribute := range block.Attributes {
		path := joinPath(prefix, name)
		indexed.Attributes = append(indexed.Attributes, AttributeIndex{
			Path:        path,
			Type:        attribute.Type,
			Description: attribute.Description,
			Required:    attribute.Required,
			Optional:    attribute.Optional,
			Computed:    attribute.Computed,
			Sensitive:   attribute.Sensitive,
			Deprecated:  attribute.Deprecated,
		})
		if attribute.NestedType != nil {
			indexed.Blocks = append(indexed.Blocks, BlockIndex{Path: path, NestingMode: attribute.NestedType.NestingMode})
			flattenNestedAttributes(path, attribute.NestedType.Attributes, indexed)
		}
	}
	for name, nested := range block.BlockTypes {
		path := joinPath(prefix, name)
		indexed.Blocks = append(indexed.Blocks, BlockIndex{
			Path:        path,
			NestingMode: nested.NestingMode,
			MinItems:    nested.MinItems,
			MaxItems:    nested.MaxItems,
		})
		flattenBlock(path, nested.Block, indexed)
	}
}

func flattenNestedAttributes(prefix string, attributes map[string]rawAttribute, indexed *ObjectIndex) {
	for name, attribute := range attributes {
		path := joinPath(prefix, name)
		indexed.Attributes = append(indexed.Attributes, AttributeIndex{
			Path:        path,
			Type:        attribute.Type,
			Description: attribute.Description,
			Required:    attribute.Required,
			Optional:    attribute.Optional,
			Computed:    attribute.Computed,
			Sensitive:   attribute.Sensitive,
			Deprecated:  attribute.Deprecated,
		})
		if attribute.NestedType != nil {
			indexed.Blocks = append(indexed.Blocks, BlockIndex{Path: path, NestingMode: attribute.NestedType.NestingMode})
			flattenNestedAttributes(path, attribute.NestedType.Attributes, indexed)
		}
	}
}

func joinPath(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}
