package translator

import (
	"sort"

	"github.com/terra-translate/internal/catalog"
	"github.com/terra-translate/internal/ir"
)

const generatedResourceThreshold = 0.72
const generatedAttributeThreshold = 0.65

func augmentMappingsFromCatalog(mappings *CloudMappings, catalogDir string) error {
	generated, err := catalog.LoadMapping(catalogDir, mappings.SourceProvider, mappings.TargetProvider)
	if err != nil {
		return err
	}
	for _, candidate := range generated.Resources {
		if !candidate.Manual && candidate.Score < generatedResourceThreshold {
			continue
		}
		mapping, exists := mappings.Resources[candidate.SourceType]
		if exists && mapping.TargetType != candidate.TargetType {
			// Curated resource semantics always outrank generated similarity.
			continue
		}
		if !exists {
			mapping = ResourceMapping{
				SourceType:   candidate.SourceType,
				TargetType:   candidate.TargetType,
				LogicalClass: ir.UnknownResource,
				Generated:    true,
			}
		}

		existingAttributes := make(map[string]struct{}, len(mapping.AttrMaps))
		for _, attribute := range mapping.AttrMaps {
			existingAttributes[attribute.SourceAttr] = struct{}{}
		}
		for _, attribute := range candidate.Attributes {
			if !attribute.Manual && attribute.Score < generatedAttributeThreshold {
				continue
			}
			if _, exists := existingAttributes[attribute.SourcePath]; exists {
				continue
			}
			mapping.AttrMaps = append(mapping.AttrMaps, AttrMapping{
				SourceAttr: attribute.SourcePath,
				TargetAttr: attribute.TargetPath,
				IRKey:      attribute.TargetPath,
				Generated:  true,
			})
			existingAttributes[attribute.SourcePath] = struct{}{}
			mapping.TargetAttrs = append(mapping.TargetAttrs, attribute.TargetPath)
		}
		mapping.TargetAttrs = uniqueSorted(mapping.TargetAttrs)
		mappings.Resources[candidate.SourceType] = mapping
	}
	return nil
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
