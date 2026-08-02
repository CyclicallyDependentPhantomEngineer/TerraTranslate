package catalog

import (
	"encoding/json"
	"math"
	"os"
	"sort"
	"strings"
	"time"
	"unicode"
)

type objectDescriptor struct {
	name       string
	baseName   string
	category   string
	tokens     map[string]struct{}
	attributes map[string]AttributeIndex
	attrLeaves map[string]struct{}
}

func generateMappings(indexes map[string]*ProviderIndex, modules map[string]*ModuleCatalog, overrides OverrideFile, generatedAt time.Time) map[string]*MappingCatalog {
	generated := make(map[string]*MappingCatalog)
	providerNames := make([]string, 0, len(indexes))
	for name := range indexes {
		providerNames = append(providerNames, name)
	}
	sort.Strings(providerNames)
	for _, source := range providerNames {
		for _, target := range providerNames {
			if source == target {
				continue
			}
			key := source + "-to-" + target
			generated[key] = &MappingCatalog{
				FormatVersion:  FormatVersion,
				GeneratedAt:    generatedAt,
				SourceProvider: source,
				TargetProvider: target,
				Resources: generateObjectMappings(
					source, target, indexes[source].Resources, indexes[target].Resources, overrides,
				),
				DataSources: generateObjectMappings(
					source, target, indexes[source].DataSources, indexes[target].DataSources, OverrideFile{},
				),
				Modules: generateModuleMappings(modules[source], modules[target]),
			}
		}
	}
	return generated
}

func generateObjectMappings(sourceProvider, targetProvider string, source, target map[string]ObjectIndex, overrides OverrideFile) []ResourceMappingCandidate {
	targetDescriptors := make([]objectDescriptor, 0, len(target))
	for name, object := range target {
		targetDescriptors = append(targetDescriptors, describeObject(name, targetProvider, object))
	}
	sort.Slice(targetDescriptors, func(i, j int) bool { return targetDescriptors[i].name < targetDescriptors[j].name })
	overrideIndex := make(map[string]ResourceOverride)
	for _, override := range overrides.Resources {
		if override.SourceProvider == sourceProvider && override.TargetProvider == targetProvider {
			overrideIndex[override.SourceType] = override
		}
	}

	sourceNames := make([]string, 0, len(source))
	for name := range source {
		sourceNames = append(sourceNames, name)
	}
	sort.Strings(sourceNames)
	results := make([]ResourceMappingCandidate, 0, len(sourceNames))
	for _, sourceName := range sourceNames {
		sourceDescriptor := describeObject(sourceName, sourceProvider, source[sourceName])
		ranked := rankTargets(sourceDescriptor, targetDescriptors)
		override, hasOverride := overrideIndex[sourceName]
		if hasOverride {
			if targetObject, exists := target[override.TargetType]; exists {
				manualDescriptor := describeObject(override.TargetType, targetProvider, targetObject)
				ranked = prependTarget(ranked, scoredTarget{descriptor: manualDescriptor, score: 1, reasons: []string{"manual override"}})
			}
		}
		if len(ranked) == 0 {
			continue
		}
		primary := ranked[0]
		manual := hasOverride && primary.descriptor.name == override.TargetType
		var attributeOverrides map[string]string
		if manual {
			attributeOverrides = override.Attributes
		}
		candidate := ResourceMappingCandidate{
			SourceType: sourceName,
			TargetType: primary.descriptor.name,
			Score:      roundScore(primary.score),
			Confidence: confidence(primary.score),
			Manual:     manual,
			Reasons:    primary.reasons,
			Attributes: generateAttributeMappings(sourceDescriptor, primary.descriptor, attributeOverrides),
		}
		for _, alternate := range ranked[1:minInt(len(ranked), 5)] {
			candidate.Alternates = append(candidate.Alternates, TypeCandidate{
				TargetType: alternate.descriptor.name,
				Score:      roundScore(alternate.score),
				Confidence: confidence(alternate.score),
				Reasons:    alternate.reasons,
			})
		}
		results = append(results, candidate)
	}
	return results
}

type scoredTarget struct {
	descriptor objectDescriptor
	score      float64
	reasons    []string
}

func rankTargets(source objectDescriptor, targets []objectDescriptor) []scoredTarget {
	var ranked []scoredTarget
	for _, target := range targets {
		score, reasons := objectScore(source, target)
		if score < 0.20 {
			continue
		}
		ranked = append(ranked, scoredTarget{descriptor: target, score: score, reasons: reasons})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score == ranked[j].score {
			return ranked[i].descriptor.name < ranked[j].descriptor.name
		}
		return ranked[i].score > ranked[j].score
	})
	return ranked
}

func prependTarget(targets []scoredTarget, manual scoredTarget) []scoredTarget {
	result := []scoredTarget{manual}
	for _, target := range targets {
		if target.descriptor.name != manual.descriptor.name {
			result = append(result, target)
		}
	}
	return result
}

func objectScore(source, target objectDescriptor) (float64, []string) {
	var score float64
	var reasons []string
	if source.baseName == target.baseName {
		score += 0.55
		reasons = append(reasons, "same normalized type name")
	}
	if source.category != "" && source.category == target.category {
		score += 0.35
		reasons = append(reasons, "same service category: "+source.category)
	}
	tokenScore := jaccard(source.tokens, target.tokens)
	if tokenScore > 0 {
		score += 0.30 * tokenScore
		reasons = append(reasons, "resource-name token overlap")
	}
	if score == 0 {
		return 0, nil
	}
	attributeScore := jaccard(source.attrLeaves, target.attrLeaves)
	if attributeScore > 0 {
		score += 0.15 * attributeScore
		reasons = append(reasons, "writable attribute overlap")
	}
	return math.Min(score, 1), reasons
}

func generateAttributeMappings(source, target objectDescriptor, overrides map[string]string) []AttributeMappingCandidate {
	sourcePaths := sortedAttributePaths(source.attributes)
	targetPaths := sortedAttributePaths(target.attributes)
	usedTargets := make(map[string]struct{})
	matchedSources := make(map[string]struct{})
	results := make([]AttributeMappingCandidate, 0, len(sourcePaths))

	// Reserve manual and exact-path matches before fuzzy candidates. Without
	// this pass, an alphabetically earlier synonym can consume a canonical
	// top-level target and force the exact source attribute into a nested field.
	for _, sourcePath := range sourcePaths {
		if targetPath, ok := overrides[sourcePath]; ok {
			if _, exists := target.attributes[targetPath]; exists {
				results = append(results, AttributeMappingCandidate{
					SourcePath: sourcePath, TargetPath: targetPath, Score: 1,
					Confidence: "high", Manual: true, Reasons: []string{"manual override"},
				})
				usedTargets[targetPath] = struct{}{}
				matchedSources[sourcePath] = struct{}{}
			}
		}
	}
	for _, sourcePath := range sourcePaths {
		if _, matched := matchedSources[sourcePath]; matched {
			continue
		}
		targetAttribute, exists := target.attributes[sourcePath]
		if !exists {
			continue
		}
		if _, used := usedTargets[sourcePath]; used {
			continue
		}
		score, reasons := attributeScore(sourcePath, source.attributes[sourcePath], sourcePath, targetAttribute)
		results = append(results, AttributeMappingCandidate{
			SourcePath: sourcePath, TargetPath: sourcePath, Score: roundScore(score),
			Confidence: confidence(score), Reasons: reasons,
		})
		usedTargets[sourcePath] = struct{}{}
		matchedSources[sourcePath] = struct{}{}
	}

	for _, sourcePath := range sourcePaths {
		if _, matched := matchedSources[sourcePath]; matched {
			continue
		}

		bestPath := ""
		bestScore := 0.0
		var bestReasons []string
		for _, targetPath := range targetPaths {
			if _, used := usedTargets[targetPath]; used {
				continue
			}
			score, reasons := attributeScore(sourcePath, source.attributes[sourcePath], targetPath, target.attributes[targetPath])
			if score > bestScore || (score == bestScore && targetPath < bestPath) {
				bestPath, bestScore, bestReasons = targetPath, score, reasons
			}
		}
		if bestScore >= 0.45 {
			results = append(results, AttributeMappingCandidate{
				SourcePath: sourcePath, TargetPath: bestPath, Score: roundScore(bestScore),
				Confidence: confidence(bestScore), Reasons: bestReasons,
			})
			usedTargets[bestPath] = struct{}{}
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].SourcePath < results[j].SourcePath })
	return results
}

func attributeScore(sourcePath string, source AttributeIndex, targetPath string, target AttributeIndex) (float64, []string) {
	var score float64
	var reasons []string
	sourceLeaf, targetLeaf := pathLeaf(sourcePath), pathLeaf(targetPath)
	if sourcePath == targetPath {
		score += 0.75
		reasons = append(reasons, "same attribute path")
	} else if sourceLeaf == targetLeaf {
		score += 0.65
		reasons = append(reasons, "same attribute name")
	} else if canonicalAttribute(sourceLeaf) == canonicalAttribute(targetLeaf) {
		score += 0.60
		reasons = append(reasons, "attribute synonym")
	} else {
		overlap := jaccard(tokenize(sourceLeaf), tokenize(targetLeaf))
		if overlap > 0 {
			score += 0.45 * overlap
			reasons = append(reasons, "attribute token overlap")
		}
	}
	if compatibleTypes(source.Type, target.Type) {
		score += 0.20
		reasons = append(reasons, "compatible Terraform type")
	}
	if strings.Count(sourcePath, ".") == strings.Count(targetPath, ".") {
		score += 0.05
	}
	return math.Min(score, 1), reasons
}

func describeObject(name, provider string, object ObjectIndex) objectDescriptor {
	attributes := make(map[string]AttributeIndex)
	attributeLeaves := make(map[string]struct{})
	for _, attribute := range object.Attributes {
		if attribute.Required || attribute.Optional {
			attributes[attribute.Path] = attribute
			attributeLeaves[canonicalAttribute(pathLeaf(attribute.Path))] = struct{}{}
		}
	}
	base := strings.TrimPrefix(name, provider+"_")
	return objectDescriptor{
		name:       name,
		baseName:   canonicalResourceName(base),
		category:   resourceCategory(base),
		tokens:     tokenize(canonicalResourceName(base)),
		attributes: attributes,
		attrLeaves: attributeLeaves,
	}
}

func canonicalResourceName(name string) string {
	name = strings.ToLower(name)
	replacements := []struct{ old, new string }{
		{"cloudfunctions2_function", "function"},
		{"cloudfunctions_function", "function"},
		{"lambda_function", "function"},
		{"linux_virtual_machine", "instance"},
		{"windows_virtual_machine", "instance"},
		{"compute_instance", "instance"},
		{"storage_account", "storage_bucket"},
		{"s3_bucket", "storage_bucket"},
		{"compute_network", "network"},
		{"virtual_network", "network"},
		{"vpc", "network"},
		{"compute_subnetwork", "subnet"},
		{"network_security_group", "firewall"},
		{"security_group", "firewall"},
		{"compute_firewall", "firewall"},
		{"sql_database_instance", "database_instance"},
		{"db_instance", "database_instance"},
	}
	for _, replacement := range replacements {
		name = strings.ReplaceAll(name, replacement.old, replacement.new)
	}
	return name
}

func resourceCategory(name string) string {
	name = canonicalResourceName(name)
	categories := []struct {
		category string
		terms    []string
	}{
		{"function", []string{"function"}},
		{"instance", []string{"instance", "virtual_machine"}},
		{"storage", []string{"storage_bucket", "bucket", "blob"}},
		{"subnet", []string{"subnet", "subnetwork"}},
		{"network", []string{"network", "vpc"}},
		{"firewall", []string{"firewall", "security_group"}},
		{"database", []string{"database", "sql", "rds"}},
		{"load_balancer", []string{"load_balancer", "loadbalancer", "lb"}},
		{"dns", []string{"dns", "route53"}},
		{"identity", []string{"iam", "identity", "role_assignment"}},
		{"secret", []string{"secret", "key_vault", "keyvault"}},
		{"queue", []string{"queue", "sqs", "pubsub", "servicebus"}},
		{"kubernetes", []string{"kubernetes", "eks", "gke", "aks"}},
	}
	for _, candidate := range categories {
		for _, term := range candidate.terms {
			if strings.Contains(name, term) {
				return candidate.category
			}
		}
	}
	return ""
}

func canonicalAttribute(name string) string {
	synonyms := map[string]string{
		"tags": "labels", "labels": "labels",
		"identifier": "name", "bucket": "name", "function_name": "name",
		"region": "location", "zone": "location", "availability_zone": "location",
		"instance_type": "machine_type", "size": "machine_type", "tier": "machine_type",
		"cidr_block": "cidr", "ip_cidr_range": "cidr", "address_space": "cidr",
		"vpc_id": "network", "network_id": "network", "network": "network",
		"subnet_id": "subnet", "subnetwork": "subnet",
		"user_data": "startup_script", "metadata_startup_script": "startup_script",
	}
	if canonical, ok := synonyms[name]; ok {
		return canonical
	}
	return name
}

func tokenize(value string) map[string]struct{} {
	fields := strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return r == '_' || r == '-' || r == '.' || r == '/' || unicode.IsSpace(r)
	})
	tokens := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		if field == "" || field == "resource" || field == "data" {
			continue
		}
		tokens[field] = struct{}{}
	}
	return tokens
}

func jaccard(left, right map[string]struct{}) float64 {
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	intersection := 0
	for value := range left {
		if _, exists := right[value]; exists {
			intersection++
		}
	}
	union := len(left) + len(right) - intersection
	return float64(intersection) / float64(union)
}

func compatibleTypes(left, right json.RawMessage) bool {
	if len(left) == 0 || len(right) == 0 {
		return false
	}
	return strings.TrimSpace(string(left)) == strings.TrimSpace(string(right))
}

func sortedAttributePaths(attributes map[string]AttributeIndex) []string {
	paths := make([]string, 0, len(attributes))
	for path := range attributes {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func pathLeaf(path string) string {
	parts := strings.Split(path, ".")
	return parts[len(parts)-1]
}

func confidence(score float64) string {
	switch {
	case score >= 0.80:
		return "high"
	case score >= 0.60:
		return "medium"
	default:
		return "low"
	}
}

func roundScore(score float64) float64 {
	return math.Round(score*10000) / 10000
}

func loadOverrides(path string) (OverrideFile, error) {
	if path == "" {
		return OverrideFile{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return OverrideFile{}, nil
		}
		return OverrideFile{}, err
	}
	var overrides OverrideFile
	if err := json.Unmarshal(data, &overrides); err != nil {
		return OverrideFile{}, err
	}
	return overrides, nil
}

func generateModuleMappings(source, target *ModuleCatalog) []ModuleMappingCandidate {
	if source == nil || target == nil {
		return nil
	}
	targetByToken := make(map[string][]int)
	targetTokens := make([]map[string]struct{}, len(target.Modules))
	for index, module := range target.Modules {
		tokens := tokenize(module.Name)
		targetTokens[index] = tokens
		for token := range tokens {
			targetByToken[token] = append(targetByToken[token], index)
		}
	}
	var results []ModuleMappingCandidate
	for _, sourceModule := range source.Modules {
		sourceTokens := tokenize(sourceModule.Name)
		candidateIndexes := make(map[int]struct{})
		for token := range sourceTokens {
			for _, index := range targetByToken[token] {
				candidateIndexes[index] = struct{}{}
			}
		}
		type scoredModule struct {
			index int
			score float64
		}
		var ranked []scoredModule
		for index := range candidateIndexes {
			score := jaccard(sourceTokens, targetTokens[index])
			if strings.EqualFold(sourceModule.Name, target.Modules[index].Name) {
				score = math.Max(score, 0.95)
			}
			if score >= 0.30 {
				ranked = append(ranked, scoredModule{index: index, score: score})
			}
		}
		sort.Slice(ranked, func(i, j int) bool {
			if ranked[i].score == ranked[j].score {
				return target.Modules[ranked[i].index].ID < target.Modules[ranked[j].index].ID
			}
			return ranked[i].score > ranked[j].score
		})
		for _, candidate := range ranked[:minInt(len(ranked), 3)] {
			results = append(results, ModuleMappingCandidate{
				SourceID: sourceModule.ID,
				TargetID: target.Modules[candidate.index].ID,
				Score:    roundScore(candidate.score),
			})
		}
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].SourceID == results[j].SourceID {
			if results[i].Score == results[j].Score {
				return results[i].TargetID < results[j].TargetID
			}
			return results[i].Score > results[j].Score
		}
		return results[i].SourceID < results[j].SourceID
	})
	return results
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
