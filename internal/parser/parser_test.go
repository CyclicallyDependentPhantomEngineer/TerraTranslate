package parser

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/terra-translate/internal/ir"
)

func writeModule(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestParsePathReadsEveryTerraformFileInADirectory(t *testing.T) {
	dir := writeModule(t, map[string]string{
		"main.tf": `
resource "aws_s3_bucket" "assets" {
  bucket = "example-assets"
}
`,
		"variables.tf": `
variable "region" {
  type        = string
  default     = "us-east-1"
  description = "Deployment region"
}
`,
		"outputs.tf": `
output "bucket_id" {
  value = aws_s3_bucket.assets.id
}
`,
		// Neither of these is a .tf file and both must be ignored.
		"README.md":        "not terraform",
		"terraform.tfvars": `region = "eu-west-1"`,
	})

	module, err := New().ParsePath(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(module.Resources) != 1 {
		t.Fatalf("got %d resources, want 1", len(module.Resources))
	}
	if _, ok := module.Variables["region"]; !ok {
		t.Fatal("variable from variables.tf was not collected")
	}
	if _, ok := module.Outputs["bucket_id"]; !ok {
		t.Fatal("output from outputs.tf was not collected")
	}
}

func TestParsePathAcceptsASingleFile(t *testing.T) {
	dir := writeModule(t, map[string]string{
		"main.tf": `resource "aws_s3_bucket" "assets" { bucket = "x" }`,
	})
	module, err := New().ParsePath(filepath.Join(dir, "main.tf"))
	if err != nil {
		t.Fatal(err)
	}
	if len(module.Resources) != 1 {
		t.Fatalf("got %d resources, want 1", len(module.Resources))
	}
}

func TestParsePathReportsMissingAndInvalidInput(t *testing.T) {
	if _, err := New().ParsePath(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("a missing path must be an error")
	}

	dir := writeModule(t, map[string]string{"main.tf": `resource "aws_s3_bucket" {`})
	if _, err := New().ParsePath(dir); err == nil {
		t.Fatal("unparseable HCL must be an error, not an empty module")
	}
}

func TestParsedVariableAndOutputMetadata(t *testing.T) {
	dir := writeModule(t, map[string]string{
		"main.tf": `
variable "db_password" {
  type      = string
  sensitive = true
}

output "endpoint" {
  value       = "https://example.invalid"
  description = "Service endpoint"
  sensitive   = true
}
`,
	})
	module, err := New().ParsePath(dir)
	if err != nil {
		t.Fatal(err)
	}
	password := module.Variables["db_password"]
	if password == nil || !password.Sensitive {
		t.Fatal("sensitive = true was not carried into the IR variable")
	}
	endpoint := module.Outputs["endpoint"]
	if endpoint == nil || !endpoint.Sensitive || endpoint.Description != "Service endpoint" {
		t.Fatalf("output metadata was lost: %+v", endpoint)
	}
}

func TestClassifyResource(t *testing.T) {
	cases := map[string]ir.ResourceClass{
		"aws_instance":                    ir.ComputeInstance,
		"google_compute_instance":         ir.ComputeInstance,
		"aws_lambda_function":             ir.FunctionCompute,
		"aws_s3_bucket":                   ir.StorageBucket,
		"google_storage_bucket":           ir.StorageBucket,
		"azurerm_storage_account":         ir.StorageBucket,
		"aws_vpc":                         ir.NetworkVPC,
		"azurerm_virtual_network":         ir.NetworkVPC,
		"aws_subnet":                      ir.NetworkSubnet,
		"aws_security_group":              ir.SecurityGroup,
		"google_compute_firewall":         ir.SecurityGroup,
		"aws_lb":                          ir.LoadBalancer,
		"aws_db_instance":                 ir.DatabaseInstance,
		"azurerm_mssql_database":          ir.DatabaseInstance,
		"aws_route53_zone":                ir.DNSZone,
		"aws_iam_role":                    ir.IAMRole,
		"aws_cloudwatch_log_group":        ir.UnknownResource,
		"some_provider_unrecognised_kind": ir.UnknownResource,
	}
	for resourceType, want := range cases {
		if got := classifyResource(resourceType); got != want {
			t.Errorf("classifyResource(%q) = %q, want %q", resourceType, got, want)
		}
	}
}

func TestDatabaseInstancesAreNotClassifiedAsComputeInstances(t *testing.T) {
	// "aws_db_instance" contains "instance"; the classifier has to prefer the
	// database rule or every RDS resource would translate as a VM.
	if got := classifyResource("aws_db_instance"); got != ir.DatabaseInstance {
		t.Fatalf("aws_db_instance classified as %q", got)
	}
}

func TestParsePathClassifiesResources(t *testing.T) {
	dir := writeModule(t, map[string]string{
		"main.tf": `
resource "aws_s3_bucket" "assets" { bucket = "x" }
resource "aws_vpc" "main" { cidr_block = "10.0.0.0/16" }
`,
	})
	module, err := New().ParsePath(dir)
	if err != nil {
		t.Fatal(err)
	}
	classes := map[string]ir.ResourceClass{}
	for _, resource := range module.Resources {
		classes[resource.OriginalType] = resource.LogicalClass
	}
	if classes["aws_s3_bucket"] != ir.StorageBucket || classes["aws_vpc"] != ir.NetworkVPC {
		t.Fatalf("resources were not classified: %+v", classes)
	}
}
