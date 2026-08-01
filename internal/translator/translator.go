// The translator applies CloudMappings to an ir.Module, producing translated
// target resources with 1:N fan-out, reference rewriting, and continuous
// PID-driven fuzzy matching.
package translator

import (
	"math"
	"strings"

	"github.com/terra-translate/internal/ir"
)

// Result is the complete output of one translation pass.
type Result struct {
	SourceModule       *ir.Module
	TargetResources    []*ir.TargetResource
	Warnings           []ir.TranslationWarning
	TotalAttrs         int
	MappedAttrs        int
	Accuracy           float64 // legacy: CoverageRatio
	Score              ir.TranslationScore
	ResourceAccuracies []ResourceAccuracy
	RefGraph           *ir.RefGraph
}

// ResourceAccuracy records mapping coverage for one resource.
type ResourceAccuracy struct {
	OriginalType string
	Name         string
	Total        int
	Mapped       int
	Valid        int // attrs where value is valid in target
	Score        float64
}

// Translator applies CloudMappings to an ir.Module and produces a Result.
type Translator struct {
	mappings         *CloudMappings
	learnedMappings  map[string]string                 // "sourceType::sourceAttr" → target attr
	requiredDefaults map[string]map[string]interface{} // targetType → {attr → default}
}

// New creates a Translator.
func New(mappings *CloudMappings) *Translator {
	return &Translator{
		mappings:         mappings,
		learnedMappings:  make(map[string]string),
		requiredDefaults: buildRequiredDefaults(),
	}
}

// Translate converts all resources using current mappings + learned state.
// effort ∈ [0,1]: controls fuzzy-match aggressiveness CONTINUOUSLY.
func (t *Translator) Translate(module *ir.Module, effort float64) *Result {
	result := &Result{
		SourceModule: module,
		RefGraph:     ir.NewRefGraph(),
	}

	var totalValid int

	// Pass 1: Translate resources, build RefGraph.
	for _, res := range module.Resources {
		targets, accuracy := t.translateResource(res, effort)
		result.TargetResources = append(result.TargetResources, targets...)
		result.ResourceAccuracies = append(result.ResourceAccuracies, accuracy)
		result.TotalAttrs += accuracy.Total
		result.MappedAttrs += accuracy.Mapped
		totalValid += accuracy.Valid

		// Register in reference graph (primary target only).
		if len(targets) > 0 && targets[0].IsPrimary {
			result.RefGraph.Register(
				res.OriginalType, res.Name,
				targets[0].ProviderType, targets[0].Name,
			)
		}

		// Warn on missing required attrs.
		rm, hasMapping := t.mappings.Resources[res.OriginalType]
		if hasMapping {
			for _, am := range rm.AttrMaps {
				if am.Required {
					if _, exists := res.Properties[am.SourceAttr]; !exists {
						result.Warnings = append(result.Warnings, ir.TranslationWarning{
							Resource:  res.OriginalType + "." + res.Name,
							Attribute: am.SourceAttr,
							Message:   "required attribute missing from source",
							Severity:  ir.WarnMissing,
						})
					}
				}
			}
		}
	}

	// Pass 2: Rewrite cross-resource references.
	for _, tr := range result.TargetResources {
		rewriteRefs(tr, result.RefGraph)
	}

	// Pass 3: Inject required defaults for target provider.
	for _, tr := range result.TargetResources {
		t.injectRequiredDefaults(tr, result)
	}

	// Compute scores.
	if result.TotalAttrs > 0 {
		result.Accuracy = float64(result.MappedAttrs) / float64(result.TotalAttrs)
		result.Score.CoverageRatio = result.Accuracy
		if result.MappedAttrs > 0 {
			result.Score.ValidityRatio = float64(totalValid) / float64(result.MappedAttrs)
		}
		result.Score.SemanticRatio = result.Score.ValidityRatio * 0.9
	} else {
		result.Accuracy = 1.0
		result.Score.CoverageRatio = 1.0
		result.Score.ValidityRatio = 1.0
		result.Score.SemanticRatio = 1.0
	}
	result.Score.ComputeComposite()

	return result
}

func (t *Translator) translateResource(res *ir.Resource, effort float64) ([]*ir.TargetResource, ResourceAccuracy) {
	acc := ResourceAccuracy{
		OriginalType: res.OriginalType,
		Name:         res.Name,
		Total:        len(res.Properties),
	}

	rm, hasMapping := t.mappings.Resources[res.OriginalType]
	if !hasMapping {
		target := &ir.TargetResource{
			OriginalResource: res,
			ProviderType:     "# unmapped:" + res.OriginalType,
			Name:             res.Name,
			Attributes:       make(map[string]interface{}),
			NestedBlocks:     make(map[string]interface{}),
			TodoAttrs:        make(map[string]string),
			Comment:          "No mapping found for " + res.OriginalType,
			IsPrimary:        true,
		}
		for k := range res.Properties {
			target.UnmappedAttrs = append(target.UnmappedAttrs, k)
		}
		return []*ir.TargetResource{target}, acc
	}

	// 1:N expansion.
	if rm.Expand != nil {
		expanded := rm.Expand(res)
		for _, tr := range expanded {
			acc.Mapped += len(tr.Attributes) + len(tr.NestedBlocks)
			acc.Valid += len(tr.Attributes) + len(tr.NestedBlocks)
		}
		if acc.Total > 0 {
			acc.Score = float64(acc.Mapped) / float64(acc.Total)
		}
		return expanded, acc
	}

	// Standard 1:1 translation.
	target := &ir.TargetResource{
		OriginalResource: res,
		ProviderType:     rm.TargetType,
		Name:             res.Name,
		Attributes:       make(map[string]interface{}),
		NestedBlocks:     make(map[string]interface{}),
		TodoAttrs:        make(map[string]string),
		IsPrimary:        true,
	}

	attrIndex := make(map[string]AttrMapping, len(rm.AttrMaps))
	for _, am := range rm.AttrMaps {
		attrIndex[am.SourceAttr] = am
	}

	// Continuous fuzzy threshold from effort.
	fuzzyThreshold := int(math.Round(8.0 * (1.0 - effort)))
	if fuzzyThreshold < 1 {
		fuzzyThreshold = 1
	}

	for srcKey, prop := range res.Properties {
		mapped := false

		// 1. Exact AttrMap match.
		if am, ok := attrIndex[srcKey]; ok {
			val := prop.Value
			if am.Transform != nil {
				val = am.Transform(val)
			}
			setNestedAttr(target, am.TargetAttr, val)
			acc.Mapped++
			if isValueValid(am, val) {
				acc.Valid++
			} else {
				target.TodoAttrs[am.TargetAttr] = "value may be invalid in target provider"
			}
			mapped = true
		}

		// 2. Learned mappings (integral term memory).
		if !mapped {
			learnKey := res.OriginalType + "::" + srcKey
			if targetAttr, ok := t.learnedMappings[learnKey]; ok {
				setNestedAttr(target, targetAttr, prop.Value)
				acc.Mapped++
				target.TodoAttrs[targetAttr] = "fuzzy-matched; verify value"
				mapped = true
			}
		}

		// 3. Fuzzy matching — continuous aggressiveness.
		if !mapped && effort > 0.1 {
			if targetAttr, dist, ok := t.fuzzyMatchScored(srcKey, rm, fuzzyThreshold); ok {
				setNestedAttr(target, targetAttr, prop.Value)
				acc.Mapped++
				t.learnedMappings[res.OriginalType+"::"+srcKey] = targetAttr
				target.TodoAttrs[targetAttr] = "fuzzy-matched (distance=" +
					strings.Repeat("~", dist) + "); verify value and semantics"
				mapped = true
			}
		}

		if !mapped {
			target.UnmappedAttrs = append(target.UnmappedAttrs, srcKey)
		}
	}

	if acc.Total > 0 {
		acc.Score = float64(acc.Mapped) / float64(acc.Total)
	}
	return []*ir.TargetResource{target}, acc
}

func setNestedAttr(target *ir.TargetResource, attrPath string, value interface{}) {
	if !strings.Contains(attrPath, ".") {
		target.Attributes[attrPath] = value
		return
	}
	target.NestedBlocks[attrPath] = value
}

// rewriteRefs walks all string values in a TargetResource and transforms
// "${aws_vpc.main.id}" → "${google_compute_network.main.self_link}".
func rewriteRefs(tr *ir.TargetResource, rg *ir.RefGraph) {
	for k, v := range tr.Attributes {
		if s, ok := v.(string); ok && isRef(s) {
			tr.Attributes[k] = rewriteRefString(s, rg)
		}
	}
	for k, v := range tr.NestedBlocks {
		if s, ok := v.(string); ok && isRef(s) {
			tr.NestedBlocks[k] = rewriteRefString(s, rg)
		}
	}
}

func isRef(s string) bool {
	return strings.HasPrefix(s, "${") && strings.HasSuffix(s, "}")
}

func rewriteRefString(ref string, rg *ir.RefGraph) string {
	inner := ref[2 : len(ref)-1]
	rewritten := rg.Rewrite(inner)
	return "${" + rewritten + "}"
}

// fuzzyMatchScored returns the best Levenshtein match within the threshold.
func (t *Translator) fuzzyMatchScored(srcKey string, rm ResourceMapping, maxDist int) (string, int, bool) {
	srcLower := strings.ToLower(srcKey)
	bestAttr := ""
	bestDist := maxDist + 1

	for _, am := range rm.AttrMaps {
		tgtParts := strings.Split(am.TargetAttr, ".")
		tgtLeaf := strings.ToLower(tgtParts[len(tgtParts)-1])
		dist := levenshtein(srcLower, tgtLeaf)
		if dist < bestDist {
			bestDist = dist
			bestAttr = am.TargetAttr
		}
	}

	// Synonym check.
	synonyms := map[string]string{
		"tags": "labels", "labels": "tags",
		"name": "identifier", "identifier": "name",
		"region": "location", "location": "region",
	}
	if syn, ok := synonyms[srcLower]; ok {
		for _, am := range rm.AttrMaps {
			tgtParts := strings.Split(am.TargetAttr, ".")
			tgtLeaf := strings.ToLower(tgtParts[len(tgtParts)-1])
			if tgtLeaf == syn {
				return am.TargetAttr, 0, true
			}
		}
	}

	if bestDist <= maxDist {
		return bestAttr, bestDist, true
	}
	return "", 0, false
}

func levenshtein(a, b string) int {
	la, lb := len(a), len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min3(curr[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[lb]
}

func min3(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}

// isValueValid checks if a transformed value is plausible for the target attribute.
func isValueValid(am AttrMapping, val interface{}) bool {
	if val == nil {
		return !am.Required
	}
	s, ok := val.(string)
	if !ok {
		return true
	}
	invalidPrefixes := []string{"ami-", "arn:aws:", "sg-", "vpc-", "subnet-", "i-0"}
	for _, prefix := range invalidPrefixes {
		if strings.HasPrefix(s, prefix) {
			return false
		}
	}
	return true
}

// injectRequiredDefaults adds mandatory target-provider attributes with defaults.
func (t *Translator) injectRequiredDefaults(tr *ir.TargetResource, result *Result) {
	defaults, ok := t.requiredDefaults[tr.ProviderType]
	if !ok {
		return
	}
	for attr, defaultVal := range defaults {
		if _, hasFlat := tr.Attributes[attr]; hasFlat {
			continue
		}
		if _, hasNested := tr.NestedBlocks[attr]; hasNested {
			continue
		}
		if strings.Contains(attr, ".") {
			tr.NestedBlocks[attr] = defaultVal
		} else {
			tr.Attributes[attr] = defaultVal
		}
		tr.TodoAttrs[attr] = "auto-injected default; must replace with real value"
		result.Warnings = append(result.Warnings, ir.TranslationWarning{
			Resource:  tr.ProviderType + "." + tr.Name,
			Attribute: attr,
			Message:   "required attribute injected with default — must be customised",
			Severity:  ir.WarnManual,
		})
	}
}

func buildRequiredDefaults() map[string]map[string]interface{} {
	return map[string]map[string]interface{}{
		"google_compute_instance": {
			"name":                              "TODO-name",
			"machine_type":                      "e2-medium",
			"boot_disk.initialize_params.image": "debian-cloud/debian-12",
			"network_interface.network":         "default",
		},
		"google_compute_network": {
			"name":                    "TODO-network-name",
			"auto_create_subnetworks": false,
		},
		"google_compute_subnetwork": {
			"name":          "TODO-subnet-name",
			"ip_cidr_range": "10.0.0.0/24",
			"network":       "TODO-network-self-link",
		},
		"google_compute_firewall": {
			"name":    "TODO-firewall-name",
			"network": "TODO-network-self-link",
		},
		"google_storage_bucket": {
			"name":     "TODO-bucket-name",
			"location": "US",
		},
		"google_sql_database_instance": {
			"name":             "TODO-db-name",
			"database_version": "POSTGRES_14",
			"settings.tier":    "db-f1-micro",
		},
		"google_cloudfunctions2_function": {
			"name":     "TODO-function-name",
			"location": "us-central1",
		},
		"azurerm_linux_virtual_machine": {
			"name":                "TODO-vm-name",
			"size":                "Standard_B2s",
			"resource_group_name": "TODO-resource-group",
			"location":            "East US",
			"admin_username":      "adminuser",
		},
		"azurerm_storage_account": {
			"name":                     "todostorageacct",
			"resource_group_name":      "TODO-resource-group",
			"location":                 "East US",
			"account_tier":             "Standard",
			"account_replication_type": "LRS",
		},
		"azurerm_virtual_network": {
			"name":                "TODO-vnet-name",
			"resource_group_name": "TODO-resource-group",
			"location":            "East US",
		},
	}
}

// LearnFromMissed builds learned mappings from unmapped attributes.
func (t *Translator) LearnFromMissed(missed []UnmappedAttr, effort float64) {
	if effort < 0.2 {
		return
	}
	maxDist := int(math.Round(6.0 * effort))
	if maxDist < 1 {
		maxDist = 1
	}
	for _, m := range missed {
		learnKey := m.SourceType + "::" + m.SourceAttr
		if _, already := t.learnedMappings[learnKey]; already {
			continue
		}
		rm, ok := t.mappings.Resources[m.SourceType]
		if !ok {
			continue
		}
		srcBare := normaliseName(m.SourceAttr)
		for _, am := range rm.AttrMaps {
			if normaliseName(am.TargetAttr) == srcBare {
				t.learnedMappings[learnKey] = am.TargetAttr
				break
			}
		}
		if _, found := t.learnedMappings[learnKey]; !found {
			bestDist := maxDist + 1
			bestAttr := ""
			for _, am := range rm.AttrMaps {
				parts := strings.Split(am.TargetAttr, ".")
				leaf := parts[len(parts)-1]
				dist := levenshtein(strings.ToLower(m.SourceAttr), strings.ToLower(leaf))
				if dist < bestDist {
					bestDist = dist
					bestAttr = am.TargetAttr
				}
			}
			if bestDist <= maxDist {
				t.learnedMappings[learnKey] = bestAttr
			}
		}
	}
}

// UnmappedAttr carries context about an attribute that could not be mapped.
type UnmappedAttr struct {
	SourceType string
	SourceAttr string
	Value      interface{}
}

func normaliseName(name string) string {
	name = strings.ToLower(name)
	for _, prefix := range []string{"aws_", "google_", "azurerm_", "az_"} {
		name = strings.TrimPrefix(name, prefix)
	}
	for _, suffix := range []string{"_id", "_name", "_enabled", "_config"} {
		name = strings.TrimSuffix(name, suffix)
	}
	return strings.ReplaceAll(name, "_", "")
}
