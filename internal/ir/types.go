// Package ir defines the cloud-agnostic intermediate representation (IR).
//
// Design:
//   - TargetResource supports 1:N resource fan-out
//   - TranslationScore provides coverage + validity + semantics dimensions
//   - RefGraph tracks cross-resource reference rewriting
//   - TodoAttrs mark attributes needing manual review
package ir

// Module is the top-level IR for a complete Terraform configuration directory.
type Module struct {
	SourceProvider string
	Resources      []*Resource
	Variables      map[string]*Variable
	Outputs        map[string]*Output
	Locals         map[string]interface{}
	DataSources    []*DataSource
}

// ResourceClass is a vendor-neutral semantic category.
type ResourceClass string

const (
	ComputeInstance  ResourceClass = "compute.instance"
	ComputeDisk      ResourceClass = "compute.disk"
	StorageBucket    ResourceClass = "storage.bucket"
	StorageIAM       ResourceClass = "storage.iam"
	NetworkVPC       ResourceClass = "network.vpc"
	NetworkSubnet    ResourceClass = "network.subnet"
	SecurityGroup    ResourceClass = "network.security_group"
	FirewallRule     ResourceClass = "network.firewall_rule"
	DatabaseInstance ResourceClass = "database.instance"
	DatabaseUser     ResourceClass = "database.user"
	DatabaseDB       ResourceClass = "database.database"
	LoadBalancer     ResourceClass = "network.load_balancer"
	DNSZone          ResourceClass = "dns.zone"
	IAMRole          ResourceClass = "iam.role"
	IAMBinding       ResourceClass = "iam.binding"
	FunctionCompute  ResourceClass = "compute.function"
	KVStore          ResourceClass = "storage.kv"
	UnknownResource  ResourceClass = "unknown"
)

// Resource is the IR representation of a single Terraform resource block.
type Resource struct {
	OriginalType string               // e.g. "aws_instance"
	LogicalClass ResourceClass        // e.g. ComputeInstance
	Name         string               // Terraform logical name
	Properties   map[string]*Property // attribute name → Property
	Meta         ResourceMeta
}

// Property holds a single resource attribute.
type Property struct {
	Name       string
	IRKey      string
	Value      interface{}
	Type       PropertyType
	Required   bool
	SourceAttr string
	// RawSource is the original HCL source text for expressions that can't
	// be statically evaluated. Used for reference rewriting.
	RawSource string
}

// PropertyType is the data type of a Property value.
type PropertyType string

const (
	TypeString PropertyType = "string"
	TypeNumber PropertyType = "number"
	TypeBool   PropertyType = "bool"
	TypeList   PropertyType = "list"
	TypeMap    PropertyType = "map"
	TypeObject PropertyType = "object"
	TypeRef    PropertyType = "reference" // cross-resource reference
)

// ResourceMeta holds lifecycle and meta-argument data.
type ResourceMeta struct {
	DependsOn           []string
	Count               interface{}
	ForEach             interface{}
	LifecycleIgnore     []string
	CreateBeforeDestroy bool
	PreventDestroy      bool
}

// Variable represents a Terraform input variable.
type Variable struct {
	Name        string
	Type        string
	Default     interface{}
	Description string
	Sensitive   bool
}

// Output represents a Terraform output value.
type Output struct {
	Name        string
	Value       interface{}
	Description string
	Sensitive   bool
}

// DataSource represents a data block.
type DataSource struct {
	Type       string
	Name       string
	Properties map[string]*Property
}

// TargetResource is the result of mapping one IR Resource to ONE target resource.
// A single source resource may produce multiple TargetResources (1:N fan-out).
type TargetResource struct {
	OriginalResource *Resource
	ProviderType     string                 // e.g. "google_compute_instance"
	Name             string                 // Terraform logical name
	Attributes       map[string]interface{} // flat target attributes
	NestedBlocks     map[string]interface{} // dotted-path nested block attrs
	UnmappedAttrs    []string               // source attrs that couldn't be mapped
	Comment          string                 // translation notes
	IsPrimary        bool                   // true for the main resource in a 1:N expansion
	// TodoAttrs are attributes that were mapped structurally but whose VALUES
	// are invalid in the target provider and need manual replacement.
	TodoAttrs map[string]string // attr → "TODO: ..." message
}

// TranslationWarning is a non-fatal issue found during translation.
type TranslationWarning struct {
	Resource  string
	Attribute string
	Message   string
	Severity  WarningSeverity
}

// WarningSeverity levels.
type WarningSeverity int

const (
	WarnInfo     WarningSeverity = iota // Informational
	WarnManual                          // Needs manual review
	WarnSemantic                        // Semantic mismatch (value may be wrong)
	WarnMissing                         // No mapping exists
)

// ── Scoring ──────────────────────────────────────────────────────────────────

// TranslationScore provides three orthogonal dimensions for the PID controller
// instead of a single flat "accuracy" metric.
type TranslationScore struct {
	// CoverageRatio: fraction of source attrs that were mapped to any target attr.
	CoverageRatio float64
	// ValidityRatio: fraction of mapped attrs whose VALUE is valid in the target
	// provider (e.g. "e2-medium" is valid for GCP machine_type, "ami-xxx" is not).
	ValidityRatio float64
	// SemanticRatio: fraction of mapped attrs where the mapping preserves the
	// original intent (e.g. multi_az=true → availability_type="REGIONAL" is
	// semantically correct; username → root_password is not).
	SemanticRatio float64
	// Composite is the weighted score used as the PID process variable.
	Composite float64
}

// ComputeComposite calculates the weighted composite score.
func (s *TranslationScore) ComputeComposite() {
	s.Composite = 0.4*s.CoverageRatio + 0.35*s.ValidityRatio + 0.25*s.SemanticRatio
}

// ── Reference Graph ──────────────────────────────────────────────────────────

// RefGraph tracks how resource identifiers change across translation.
// Used to rewrite cross-resource references like "${aws_vpc.main.id}" →
// "${google_compute_network.main.self_link}".
type RefGraph struct {
	// ResourceRenames: "aws_vpc.main" → "google_compute_network.main"
	ResourceRenames map[string]string
	// AttrRenames: "aws_vpc.main.id" → "google_compute_network.main.self_link"
	AttrRenames map[string]string
}

// NewRefGraph creates an empty reference graph.
func NewRefGraph() *RefGraph {
	return &RefGraph{
		ResourceRenames: make(map[string]string),
		AttrRenames:     make(map[string]string),
	}
}

// Register records a resource type/name translation.
func (rg *RefGraph) Register(sourceType, sourceName, targetType, targetName string) {
	srcKey := sourceType + "." + sourceName
	tgtKey := targetType + "." + targetName
	rg.ResourceRenames[srcKey] = tgtKey
}

// RegisterAttr records an attribute-level translation.
func (rg *RefGraph) RegisterAttr(srcFull, tgtFull string) {
	rg.AttrRenames[srcFull] = tgtFull
}

// Rewrite transforms a reference path like "aws_vpc.main.id" into
// the target provider equivalent. Returns the input unchanged if no
// mapping is found.
func (rg *RefGraph) Rewrite(ref string) string {
	// Check full attr match first (most specific).
	if rewritten, ok := rg.AttrRenames[ref]; ok {
		return rewritten
	}
	// Try resource-level match with suffix mapping.
	for srcPrefix, tgtPrefix := range rg.ResourceRenames {
		if len(ref) > len(srcPrefix) && ref[:len(srcPrefix)] == srcPrefix && ref[len(srcPrefix)] == '.' {
			suffix := ref[len(srcPrefix):]
			suffix = mapAttrSuffix(suffix)
			return tgtPrefix + suffix
		}
	}
	return ref
}

// mapAttrSuffix translates common trailing attributes between providers.
func mapAttrSuffix(suffix string) string {
	table := map[string]string{
		".id":         ".self_link",
		".arn":        ".id",
		".public_ip":  ".network_interface.0.access_config.0.nat_ip",
		".private_ip": ".network_interface.0.network_ip",
	}
	if mapped, ok := table[suffix]; ok {
		return mapped
	}
	return suffix
}
