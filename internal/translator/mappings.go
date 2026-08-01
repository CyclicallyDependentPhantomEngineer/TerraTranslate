// Package translator contains cross-provider resource and attribute mappings.
package translator

import (
	"fmt"
	"strings"

	"github.com/terra-translate/internal/ir"
)

// AttrMapping defines how one provider attribute maps to another.
type AttrMapping struct {
	SourceAttr string
	TargetAttr string
	IRKey      string
	Transform  func(interface{}) interface{} // nil means pass-through
	Required   bool
}

// ResourceMapping describes the full translation between equivalent resource
// types in two providers. If Expand is non-nil, it produces 1:N fan-out.
type ResourceMapping struct {
	SourceType   string
	TargetType   string
	LogicalClass ir.ResourceClass
	AttrMaps     []AttrMapping
	Expand       func(*ir.Resource) []*ir.TargetResource // optional 1:N
}

// CloudMappings is the full mapping table between two providers.
type CloudMappings struct {
	SourceProvider string
	TargetProvider string
	Resources      map[string]ResourceMapping
}

// LoadMappings returns the mapping table for a source → target pair.
func LoadMappings(source, target, schemaPath string) (*CloudMappings, error) {
	key := source + "->" + target
	switch key {
	case "aws->google", "aws->gcp":
		return awsToGCPMappings(), nil
	case "aws->azurerm", "aws->azure":
		return awsToAzureMappings(), nil
	case "google->aws", "gcp->aws":
		return gcpToAWSMappings(), nil
	default:
		return &CloudMappings{
			SourceProvider: source,
			TargetProvider: target,
			Resources:      make(map[string]ResourceMapping),
		}, nil
	}
}

// ── AWS → GCP ────────────────────────────────────────────────────────────────

func awsToGCPMappings() *CloudMappings {
	return &CloudMappings{
		SourceProvider: "aws",
		TargetProvider: "google",
		Resources: map[string]ResourceMapping{

			"aws_instance": {
				SourceType: "aws_instance", TargetType: "google_compute_instance",
				LogicalClass: ir.ComputeInstance,
				AttrMaps: []AttrMapping{
					{SourceAttr: "ami", TargetAttr: "boot_disk.initialize_params.image", IRKey: "image",
						Transform: func(v interface{}) interface{} {
							if s, ok := v.(string); ok && strings.HasPrefix(s, "ami-") {
								return "TODO:resolve-ami:" + s
							}
							return v
						}},
					{SourceAttr: "instance_type", TargetAttr: "machine_type", IRKey: "machine_type",
						Transform: awsInstanceToGCPMachine, Required: true},
					{SourceAttr: "tags", TargetAttr: "labels", IRKey: "labels"},
					{SourceAttr: "subnet_id", TargetAttr: "network_interface.subnetwork", IRKey: "subnet"},
					{SourceAttr: "user_data", TargetAttr: "metadata_startup_script", IRKey: "startup_script"},
					{SourceAttr: "availability_zone", TargetAttr: "zone", IRKey: "zone"},
				},
			},

			"aws_s3_bucket": {
				SourceType: "aws_s3_bucket", TargetType: "google_storage_bucket",
				LogicalClass: ir.StorageBucket,
				AttrMaps: []AttrMapping{
					{SourceAttr: "bucket", TargetAttr: "name", IRKey: "name", Required: true},
					{SourceAttr: "region", TargetAttr: "location", IRKey: "location", Transform: awsRegionToGCPLocation},
					{SourceAttr: "tags", TargetAttr: "labels", IRKey: "labels"},
					{SourceAttr: "acl", TargetAttr: "predefined_acl", IRKey: "acl", Transform: awsACLToGCP},
					{SourceAttr: "force_destroy", TargetAttr: "force_destroy", IRKey: "force_destroy"},
				},
			},

			"aws_vpc": {
				SourceType: "aws_vpc", TargetType: "google_compute_network",
				LogicalClass: ir.NetworkVPC,
				AttrMaps: []AttrMapping{
					{SourceAttr: "tags", TargetAttr: "description", IRKey: "description"},
				},
			},

			"aws_subnet": {
				SourceType: "aws_subnet", TargetType: "google_compute_subnetwork",
				LogicalClass: ir.NetworkSubnet,
				AttrMaps: []AttrMapping{
					{SourceAttr: "cidr_block", TargetAttr: "ip_cidr_range", IRKey: "cidr", Required: true},
					{SourceAttr: "vpc_id", TargetAttr: "network", IRKey: "network"},
					{SourceAttr: "availability_zone", TargetAttr: "region", IRKey: "region", Transform: awsAZToGCPRegion},
					{SourceAttr: "tags", TargetAttr: "description", IRKey: "description"},
				},
			},

			"aws_security_group": {
				SourceType: "aws_security_group", TargetType: "google_compute_firewall",
				LogicalClass: ir.SecurityGroup,
				Expand:       expandAWSSGToGCPFirewall,
				AttrMaps: []AttrMapping{
					{SourceAttr: "name", TargetAttr: "name", IRKey: "name", Required: true},
					{SourceAttr: "description", TargetAttr: "description", IRKey: "description"},
					{SourceAttr: "vpc_id", TargetAttr: "network", IRKey: "network", Required: true},
					{SourceAttr: "tags", TargetAttr: "target_tags", IRKey: "tags"},
				},
			},

			"aws_db_instance": {
				SourceType: "aws_db_instance", TargetType: "google_sql_database_instance",
				LogicalClass: ir.DatabaseInstance,
				Expand:       expandAWSDBToGCPSQL,
				AttrMaps: []AttrMapping{
					{SourceAttr: "identifier", TargetAttr: "name", IRKey: "name", Required: true},
					{SourceAttr: "engine", TargetAttr: "database_version", IRKey: "db_version", Transform: awsEngineToGCPVersion},
					{SourceAttr: "instance_class", TargetAttr: "settings.tier", IRKey: "machine_type", Transform: awsDBClassToGCPTier},
					{SourceAttr: "allocated_storage", TargetAttr: "settings.disk_size", IRKey: "disk_size"},
					{SourceAttr: "multi_az", TargetAttr: "settings.availability_type", IRKey: "availability", Transform: awsMultiAZToGCPAvailability},
					{SourceAttr: "tags", TargetAttr: "settings.user_labels", IRKey: "labels"},
				},
			},

			"aws_lambda_function": {
				SourceType: "aws_lambda_function", TargetType: "google_cloudfunctions2_function",
				LogicalClass: ir.FunctionCompute,
				AttrMaps: []AttrMapping{
					{SourceAttr: "function_name", TargetAttr: "name", IRKey: "name", Required: true},
					{SourceAttr: "runtime", TargetAttr: "build_config.runtime", IRKey: "runtime", Transform: awsRuntimeToGCPRuntime},
					{SourceAttr: "handler", TargetAttr: "build_config.entry_point", IRKey: "handler"},
					{SourceAttr: "memory_size", TargetAttr: "service_config.available_memory", IRKey: "memory",
						Transform: func(v interface{}) interface{} {
							if f, ok := v.(float64); ok {
								return fmt.Sprintf("%dMi", int(f))
							}
							return "256Mi"
						}},
					{SourceAttr: "timeout", TargetAttr: "service_config.timeout_seconds", IRKey: "timeout"},
					{SourceAttr: "tags", TargetAttr: "labels", IRKey: "labels"},
				},
			},
		},
	}
}

// ── AWS → Azure ──────────────────────────────────────────────────────────────

func awsToAzureMappings() *CloudMappings {
	return &CloudMappings{
		SourceProvider: "aws",
		TargetProvider: "azurerm",
		Resources: map[string]ResourceMapping{
			"aws_instance": {
				SourceType: "aws_instance", TargetType: "azurerm_linux_virtual_machine",
				LogicalClass: ir.ComputeInstance,
				AttrMaps: []AttrMapping{
					{SourceAttr: "instance_type", TargetAttr: "size", IRKey: "machine_type", Transform: awsInstanceToAzureSize, Required: true},
					{SourceAttr: "tags", TargetAttr: "tags", IRKey: "labels"},
					{SourceAttr: "user_data", TargetAttr: "custom_data", IRKey: "startup_script"},
					{SourceAttr: "availability_zone", TargetAttr: "zone", IRKey: "zone"},
				},
			},
			"aws_s3_bucket": {
				SourceType: "aws_s3_bucket", TargetType: "azurerm_storage_account",
				LogicalClass: ir.StorageBucket,
				AttrMaps: []AttrMapping{
					{SourceAttr: "bucket", TargetAttr: "name", IRKey: "name", Required: true},
					{SourceAttr: "region", TargetAttr: "location", IRKey: "location", Transform: awsRegionToAzureLocation},
					{SourceAttr: "tags", TargetAttr: "tags", IRKey: "labels"},
				},
			},
			"aws_vpc": {
				SourceType: "aws_vpc", TargetType: "azurerm_virtual_network",
				LogicalClass: ir.NetworkVPC,
				AttrMaps: []AttrMapping{
					{SourceAttr: "cidr_block", TargetAttr: "address_space", IRKey: "cidr",
						Transform: func(v interface{}) interface{} {
							if s, ok := v.(string); ok {
								return []interface{}{s}
							}
							return v
						}, Required: true},
					{SourceAttr: "tags", TargetAttr: "tags", IRKey: "labels"},
				},
			},
			"aws_security_group": {
				SourceType: "aws_security_group", TargetType: "azurerm_network_security_group",
				LogicalClass: ir.SecurityGroup,
				AttrMaps: []AttrMapping{
					{SourceAttr: "name", TargetAttr: "name", IRKey: "name", Required: true},
					{SourceAttr: "tags", TargetAttr: "tags", IRKey: "labels"},
				},
			},
			"aws_db_instance": {
				SourceType: "aws_db_instance", TargetType: "azurerm_sql_server",
				LogicalClass: ir.DatabaseInstance,
				AttrMaps: []AttrMapping{
					{SourceAttr: "identifier", TargetAttr: "name", IRKey: "name", Required: true},
					{SourceAttr: "username", TargetAttr: "administrator_login", IRKey: "admin_user"},
					{SourceAttr: "tags", TargetAttr: "tags", IRKey: "labels"},
					{SourceAttr: "engine_version", TargetAttr: "version", IRKey: "db_version"},
				},
			},
		},
	}
}

// ── GCP → AWS ────────────────────────────────────────────────────────────────

func gcpToAWSMappings() *CloudMappings {
	return &CloudMappings{
		SourceProvider: "google",
		TargetProvider: "aws",
		Resources: map[string]ResourceMapping{
			"google_compute_instance": {
				SourceType: "google_compute_instance", TargetType: "aws_instance",
				LogicalClass: ir.ComputeInstance,
				AttrMaps: []AttrMapping{
					{SourceAttr: "machine_type", TargetAttr: "instance_type", IRKey: "machine_type", Transform: gcpMachineToAWSInstance, Required: true},
					{SourceAttr: "labels", TargetAttr: "tags", IRKey: "labels"},
					{SourceAttr: "zone", TargetAttr: "availability_zone", IRKey: "zone"},
					{SourceAttr: "metadata_startup_script", TargetAttr: "user_data", IRKey: "startup_script"},
				},
			},
			"google_storage_bucket": {
				SourceType: "google_storage_bucket", TargetType: "aws_s3_bucket",
				LogicalClass: ir.StorageBucket,
				AttrMaps: []AttrMapping{
					{SourceAttr: "name", TargetAttr: "bucket", IRKey: "name", Required: true},
					{SourceAttr: "location", TargetAttr: "region", IRKey: "location", Transform: gcpLocationToAWSRegion},
					{SourceAttr: "labels", TargetAttr: "tags", IRKey: "labels"},
					{SourceAttr: "force_destroy", TargetAttr: "force_destroy", IRKey: "force_destroy"},
				},
			},
		},
	}
}

// ── 1:N Expansion Functions ──────────────────────────────────────────────────

func expandAWSSGToGCPFirewall(res *ir.Resource) []*ir.TargetResource {
	var targets []*ir.TargetResource
	vpcRef := ""
	if p, ok := res.Properties["vpc_id"]; ok {
		if s, ok := p.Value.(string); ok {
			vpcRef = s
		}
	}

	if ingressProp, ok := res.Properties["ingress"]; ok {
		rules := extractRules(ingressProp.Value)
		for i, rule := range rules {
			t := &ir.TargetResource{
				OriginalResource: res, ProviderType: "google_compute_firewall",
				Name: fmt.Sprintf("%s_ingress_%d", res.Name, i),
				Attributes: map[string]interface{}{
					"name":      fmt.Sprintf("%s-ingress-%d", res.Name, i),
					"network":   vpcRef,
					"direction": "INGRESS",
				},
				NestedBlocks: make(map[string]interface{}),
				TodoAttrs:    make(map[string]string),
				IsPrimary:    i == 0,
				Comment:      fmt.Sprintf("Ingress rule %d from aws_security_group.%s", i, res.Name),
			}
			if cidr, ok := rule["cidr_blocks"]; ok {
				t.Attributes["source_ranges"] = cidr
			}
			if proto, ok := rule["protocol"]; ok {
				t.NestedBlocks["allow.protocol"] = proto
				if from, ok := rule["from_port"]; ok {
					to := from
					if toVal, ok := rule["to_port"]; ok {
						to = toVal
					}
					t.NestedBlocks["allow.ports"] = []interface{}{fmt.Sprintf("%v-%v", from, to)}
				}
			}
			if tags, ok := res.Properties["tags"]; ok {
				t.Attributes["target_tags"] = tags.Value
			}
			targets = append(targets, t)
		}
	}

	if egressProp, ok := res.Properties["egress"]; ok {
		rules := extractRules(egressProp.Value)
		for i, rule := range rules {
			t := &ir.TargetResource{
				OriginalResource: res, ProviderType: "google_compute_firewall",
				Name: fmt.Sprintf("%s_egress_%d", res.Name, i),
				Attributes: map[string]interface{}{
					"name":      fmt.Sprintf("%s-egress-%d", res.Name, i),
					"network":   vpcRef,
					"direction": "EGRESS",
				},
				NestedBlocks: make(map[string]interface{}),
				TodoAttrs:    make(map[string]string),
				Comment:      fmt.Sprintf("Egress rule %d from aws_security_group.%s", i, res.Name),
			}
			if cidr, ok := rule["cidr_blocks"]; ok {
				t.Attributes["destination_ranges"] = cidr
			}
			if proto, ok := rule["protocol"]; ok {
				t.NestedBlocks["allow.protocol"] = proto
				if from, ok := rule["from_port"]; ok {
					to := from
					if toVal, ok := rule["to_port"]; ok {
						to = toVal
					}
					t.NestedBlocks["allow.ports"] = []interface{}{fmt.Sprintf("%v-%v", from, to)}
				}
			}
			targets = append(targets, t)
		}
	}

	if len(targets) == 0 {
		t := &ir.TargetResource{
			OriginalResource: res, ProviderType: "google_compute_firewall",
			Name: res.Name, IsPrimary: true,
			Attributes:   map[string]interface{}{"network": vpcRef},
			NestedBlocks: make(map[string]interface{}),
			TodoAttrs:    make(map[string]string),
			Comment:      "Translated from aws_security_group." + res.Name + " (no inline rules)",
		}
		if name, ok := res.Properties["name"]; ok {
			t.Attributes["name"] = name.Value
		}
		if desc, ok := res.Properties["description"]; ok {
			t.Attributes["description"] = desc.Value
		}
		targets = append(targets, t)
	}
	return targets
}

func expandAWSDBToGCPSQL(res *ir.Resource) []*ir.TargetResource {
	primary := &ir.TargetResource{
		OriginalResource: res, ProviderType: "google_sql_database_instance",
		Name: res.Name, IsPrimary: true,
		Attributes:   make(map[string]interface{}),
		NestedBlocks: make(map[string]interface{}),
		TodoAttrs:    make(map[string]string),
		Comment:      "Primary SQL instance from aws_db_instance." + res.Name,
	}
	if id, ok := res.Properties["identifier"]; ok {
		primary.Attributes["name"] = id.Value
	}
	if engine, ok := res.Properties["engine"]; ok {
		primary.Attributes["database_version"] = awsEngineToGCPVersion(engine.Value)
	}
	if class, ok := res.Properties["instance_class"]; ok {
		primary.NestedBlocks["settings.tier"] = awsDBClassToGCPTier(class.Value)
	}
	if storage, ok := res.Properties["allocated_storage"]; ok {
		primary.NestedBlocks["settings.disk_size"] = storage.Value
	}
	if maz, ok := res.Properties["multi_az"]; ok {
		primary.NestedBlocks["settings.availability_type"] = awsMultiAZToGCPAvailability(maz.Value)
	}
	if tags, ok := res.Properties["tags"]; ok {
		primary.NestedBlocks["settings.user_labels"] = tags.Value
	}
	if brp, ok := res.Properties["backup_retention_period"]; ok {
		if f, ok := brp.Value.(float64); ok && f > 0 {
			primary.NestedBlocks["settings.backup_configuration.enabled"] = true
		}
	}
	for _, attr := range []string{"skip_final_snapshot", "password", "final_snapshot_identifier"} {
		if _, ok := res.Properties[attr]; ok {
			primary.TodoAttrs[attr] = "AWS-only attribute; no GCP equivalent"
		}
	}

	database := &ir.TargetResource{
		OriginalResource: res, ProviderType: "google_sql_database",
		Name:         res.Name + "_db",
		Attributes:   map[string]interface{}{"name": "default", "instance": "${google_sql_database_instance." + res.Name + ".name}"},
		NestedBlocks: make(map[string]interface{}),
		TodoAttrs:    make(map[string]string),
		Comment:      "Database resource split from aws_db_instance." + res.Name,
	}

	user := &ir.TargetResource{
		OriginalResource: res, ProviderType: "google_sql_user",
		Name:         res.Name + "_user",
		Attributes:   map[string]interface{}{"instance": "${google_sql_database_instance." + res.Name + ".name}"},
		NestedBlocks: make(map[string]interface{}),
		TodoAttrs:    make(map[string]string),
		Comment:      "Database user split from aws_db_instance." + res.Name,
	}
	if username, ok := res.Properties["username"]; ok {
		user.Attributes["name"] = username.Value
	}
	if pw, ok := res.Properties["password"]; ok {
		user.Attributes["password"] = pw.Value
		user.TodoAttrs["password"] = "SENSITIVE: consider google_secret_manager_secret_version"
	}

	return []*ir.TargetResource{primary, database, user}
}

func extractRules(v interface{}) []map[string]interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		return []map[string]interface{}{val}
	case []interface{}:
		var out []map[string]interface{}
		for _, item := range val {
			if m, ok := item.(map[string]interface{}); ok {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

// ── Transform Functions ──────────────────────────────────────────────────────

func awsInstanceToGCPMachine(v interface{}) interface{} {
	t := map[string]string{
		"t2.micro": "e2-micro", "t2.small": "e2-small", "t2.medium": "e2-medium",
		"t3.micro": "e2-micro", "t3.small": "e2-small", "t3.medium": "e2-medium",
		"t3.large": "e2-standard-2", "t3.xlarge": "e2-standard-4",
		"m5.large": "n2-standard-2", "m5.xlarge": "n2-standard-4", "m5.2xlarge": "n2-standard-8",
		"m6i.large": "n2-standard-2", "m6i.xlarge": "n2-standard-4",
		"c5.large": "c2-standard-4", "c5.xlarge": "c2-standard-8",
		"r5.large": "n2-highmem-2", "r5.xlarge": "n2-highmem-4",
	}
	if s, ok := v.(string); ok {
		if m, ok := t[s]; ok {
			return m
		}
	}
	return "e2-medium"
}

func gcpMachineToAWSInstance(v interface{}) interface{} {
	t := map[string]string{
		"e2-micro": "t3.micro", "e2-small": "t3.small", "e2-medium": "t3.medium",
		"e2-standard-2": "t3.large", "e2-standard-4": "t3.xlarge",
		"n2-standard-2": "m5.large", "n2-standard-4": "m5.xlarge", "n2-standard-8": "m5.2xlarge",
		"c2-standard-4": "c5.large", "c2-standard-8": "c5.xlarge",
	}
	if s, ok := v.(string); ok {
		if m, ok := t[s]; ok {
			return m
		}
	}
	return "t3.medium"
}

func awsInstanceToAzureSize(v interface{}) interface{} {
	t := map[string]string{
		"t2.micro": "Standard_B1s", "t2.small": "Standard_B1ms", "t2.medium": "Standard_B2s",
		"t3.micro": "Standard_B1s", "t3.small": "Standard_B1ms", "t3.medium": "Standard_B2s",
		"t3.large": "Standard_B2ms", "t3.xlarge": "Standard_B4ms",
		"m5.large": "Standard_D2s_v3", "m5.xlarge": "Standard_D4s_v3", "m5.2xlarge": "Standard_D8s_v3",
		"c5.large": "Standard_F2s_v2", "c5.xlarge": "Standard_F4s_v2",
		"r5.large": "Standard_E2s_v3", "r5.xlarge": "Standard_E4s_v3",
	}
	if s, ok := v.(string); ok {
		if m, ok := t[s]; ok {
			return m
		}
	}
	return "Standard_B2s"
}

func awsRegionToGCPLocation(v interface{}) interface{} {
	t := map[string]string{
		"us-east-1": "US-EAST1", "us-east-2": "US-EAST4",
		"us-west-1": "US-WEST2", "us-west-2": "US-WEST1",
		"eu-west-1": "EUROPE-WEST1", "eu-west-2": "EUROPE-WEST2", "eu-central-1": "EUROPE-WEST3",
		"ap-southeast-1": "ASIA-SOUTHEAST1", "ap-northeast-1": "ASIA-NORTHEAST1",
		"ap-south-1": "ASIA-SOUTH1",
	}
	if s, ok := v.(string); ok {
		if m, ok := t[s]; ok {
			return m
		}
	}
	return "US-EAST1"
}

func gcpLocationToAWSRegion(v interface{}) interface{} {
	t := map[string]string{
		"US-EAST1": "us-east-1", "US-EAST4": "us-east-2",
		"US-WEST1": "us-west-2", "US-WEST2": "us-west-1",
		"EUROPE-WEST1": "eu-west-1", "EUROPE-WEST2": "eu-west-2",
		"ASIA-SOUTHEAST1": "ap-southeast-1", "ASIA-NORTHEAST1": "ap-northeast-1",
	}
	if s, ok := v.(string); ok {
		if m, ok := t[s]; ok {
			return m
		}
	}
	return "us-east-1"
}

func awsRegionToAzureLocation(v interface{}) interface{} {
	t := map[string]string{
		"us-east-1": "East US", "us-east-2": "East US 2",
		"us-west-1": "West US", "us-west-2": "West US 2",
		"eu-west-1": "West Europe", "eu-west-2": "UK South", "eu-central-1": "Germany West Central",
		"ap-southeast-1": "Southeast Asia", "ap-northeast-1": "Japan East",
	}
	if s, ok := v.(string); ok {
		if m, ok := t[s]; ok {
			return m
		}
	}
	return "East US"
}

func awsAZToGCPRegion(v interface{}) interface{} {
	if s, ok := v.(string); ok && len(s) > 0 {
		base := s[:len(s)-1]
		return awsRegionToGCPLocation(base)
	}
	return "us-east1"
}

func awsACLToGCP(v interface{}) interface{} {
	t := map[string]string{
		"private": "private", "public-read": "publicRead",
		"public-read-write": "publicReadWrite", "authenticated-read": "authenticatedRead",
	}
	if s, ok := v.(string); ok {
		if m, ok := t[s]; ok {
			return m
		}
	}
	return "private"
}

func awsEngineToGCPVersion(v interface{}) interface{} {
	t := map[string]string{"mysql": "MYSQL_8_0", "postgres": "POSTGRES_14", "mariadb": "MYSQL_8_0"}
	if s, ok := v.(string); ok {
		if m, ok := t[s]; ok {
			return m
		}
	}
	return "MYSQL_8_0"
}

func awsDBClassToGCPTier(v interface{}) interface{} {
	t := map[string]string{
		"db.t2.micro": "db-f1-micro", "db.t2.small": "db-g1-small",
		"db.t3.micro": "db-f1-micro", "db.t3.small": "db-g1-small", "db.t3.medium": "db-g1-small",
		"db.m5.large": "db-n1-standard-2", "db.m5.xlarge": "db-n1-standard-4",
		"db.r5.large": "db-n1-highmem-2", "db.r5.xlarge": "db-n1-highmem-4",
	}
	if s, ok := v.(string); ok {
		if m, ok := t[s]; ok {
			return m
		}
	}
	return "db-f1-micro"
}

func awsMultiAZToGCPAvailability(v interface{}) interface{} {
	if b, ok := v.(bool); ok && b {
		return "REGIONAL"
	}
	return "ZONAL"
}

func awsRuntimeToGCPRuntime(v interface{}) interface{} {
	t := map[string]string{
		"python3.9": "python39", "python3.10": "python310", "python3.11": "python311",
		"nodejs18.x": "nodejs18", "nodejs20.x": "nodejs20",
		"go1.x": "go121", "java11": "java11", "java17": "java17", "dotnet6": "dotnet6",
	}
	if s, ok := v.(string); ok {
		if m, ok := t[s]; ok {
			return m
		}
	}
	return v
}
