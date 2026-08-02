package translator

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

type providerSchemaDocument struct {
	ProviderSchemas map[string]providerSchema `json:"provider_schemas"`
}

type providerSchema struct {
	ResourceSchemas map[string]resourceSchema `json:"resource_schemas"`
}

type resourceSchema struct {
	Block schemaBlock `json:"block"`
}

type schemaBlock struct {
	Attributes map[string]schemaAttribute `json:"attributes"`
	BlockTypes map[string]nestedSchema    `json:"block_types"`
}

type schemaAttribute struct {
	Required bool `json:"required"`
	Optional bool `json:"optional"`
	Computed bool `json:"computed"`
}

type nestedSchema struct {
	Block schemaBlock `json:"block"`
}

func augmentMappingsFromSchema(mappings *CloudMappings, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read provider schema %q: %w", path, err)
	}
	var document providerSchemaDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("decode provider schema %q: %w", path, err)
	}
	if len(document.ProviderSchemas) == 0 {
		return fmt.Errorf("provider schema %q contains no provider_schemas", path)
	}

	resourceSchemas := make(map[string]resourceSchema)
	for _, provider := range document.ProviderSchemas {
		for resourceType, schema := range provider.ResourceSchemas {
			resourceSchemas[resourceType] = schema
		}
	}
	for sourceType, mapping := range mappings.Resources {
		schema, exists := resourceSchemas[mapping.TargetType]
		if !exists {
			continue
		}
		var attributes []string
		flattenSchemaBlock("", schema.Block, &attributes)
		sort.Strings(attributes)
		mapping.TargetAttrs = attributes
		mappings.Resources[sourceType] = mapping
	}
	return nil
}

func flattenSchemaBlock(prefix string, block schemaBlock, attributes *[]string) {
	for name, attribute := range block.Attributes {
		// Computed-only values cannot be assigned in Terraform configuration.
		if !attribute.Required && !attribute.Optional {
			continue
		}
		if prefix == "" {
			*attributes = append(*attributes, name)
		} else {
			*attributes = append(*attributes, prefix+"."+name)
		}
	}
	for name, nested := range block.BlockTypes {
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		flattenSchemaBlock(path, nested.Block, attributes)
	}
}
