// Package parser reads Terraform HCL and converts it to the IR.
package parser

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"

	"github.com/terra-translate/internal/ir"
)

// Parser reads .tf files and produces an IR Module.
type Parser struct {
	hclParser *hclparse.Parser
}

// New returns a fresh Parser.
func New() *Parser {
	return &Parser{hclParser: hclparse.NewParser()}
}

// ParsePath accepts a file or directory path. If a directory, all *.tf files
// within it (non-recursive) are parsed and merged into a single Module.
func (p *Parser) ParsePath(path string) (*ir.Module, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("cannot stat path %q: %w", path, err)
	}

	module := &ir.Module{
		Variables: make(map[string]*ir.Variable),
		Outputs:   make(map[string]*ir.Output),
		Locals:    make(map[string]interface{}),
	}

	if info.IsDir() {
		entries, err := os.ReadDir(path)
		if err != nil {
			return nil, fmt.Errorf("reading directory %q: %w", path, err)
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".tf") {
				if err := p.parseFile(filepath.Join(path, e.Name()), module); err != nil {
					return nil, err
				}
			}
		}
	} else {
		if err := p.parseFile(path, module); err != nil {
			return nil, err
		}
	}

	// Classify all resources into IR logical types.
	for _, res := range module.Resources {
		res.LogicalClass = classifyResource(res.OriginalType)
	}

	return module, nil
}

func (p *Parser) parseFile(path string, module *ir.Module) error {
	f, diags := p.hclParser.ParseHCLFile(path)
	if diags.HasErrors() {
		return fmt.Errorf("parsing %q: %s", path, diags.Error())
	}

	body, ok := f.Body.(*hclsyntax.Body)
	if !ok {
		return fmt.Errorf("unexpected body type in %q", path)
	}

	for _, block := range body.Blocks {
		switch block.Type {
		case "resource":
			if len(block.Labels) < 2 {
				continue
			}
			res := p.parseResourceBlock(block)
			module.Resources = append(module.Resources, res)

		case "data":
			if len(block.Labels) < 2 {
				continue
			}
			ds := p.parseDataSourceBlock(block)
			module.DataSources = append(module.DataSources, ds)

		case "variable":
			if len(block.Labels) < 1 {
				continue
			}
			v := p.parseVariableBlock(block)
			module.Variables[block.Labels[0]] = v

		case "output":
			if len(block.Labels) < 1 {
				continue
			}
			o := p.parseOutputBlock(block)
			module.Outputs[block.Labels[0]] = o

		case "locals":
			for name, attr := range block.Body.Attributes {
				module.Locals[name] = evalExpr(attr.Expr)
			}
		}
	}
	return nil
}

func (p *Parser) parseResourceBlock(block *hclsyntax.Block) *ir.Resource {
	res := &ir.Resource{
		OriginalType: block.Labels[0],
		Name:         block.Labels[1],
		Properties:   make(map[string]*ir.Property),
	}

	// Top-level attributes.
	for attrName, attr := range block.Body.Attributes {
		val := evalExpr(attr.Expr)
		propType := inferType(val)
		// Tag references so the translator can rewrite them.
		if propType == ir.TypeString {
			if s, ok := val.(string); ok && strings.HasPrefix(s, "${") {
				propType = ir.TypeRef
			}
		}
		res.Properties[attrName] = &ir.Property{
			Name:       attrName,
			IRKey:      attrName,
			Value:      val,
			Type:       propType,
			SourceAttr: attrName,
		}
	}

	// Top-level blocks (lifecycle, nested config blocks, etc.).
	for _, nested := range block.Body.Blocks {
		switch nested.Type {
		case "lifecycle":
			parseLifecycle(nested, &res.Meta)
		default:
			nestedVal := blockToMap(nested)
			existing, exists := res.Properties[nested.Type]
			if exists {
				// Merge into a list if the same block type appears multiple times.
				switch ev := existing.Value.(type) {
				case []interface{}:
					res.Properties[nested.Type].Value = append(ev, nestedVal)
				default:
					res.Properties[nested.Type].Value = []interface{}{ev, nestedVal}
					res.Properties[nested.Type].Type = ir.TypeList
				}
			} else {
				res.Properties[nested.Type] = &ir.Property{
					Name:       nested.Type,
					IRKey:      nested.Type,
					Value:      nestedVal,
					Type:       ir.TypeObject,
					SourceAttr: nested.Type,
				}
			}
		}
	}

	return res
}

func (p *Parser) parseDataSourceBlock(block *hclsyntax.Block) *ir.DataSource {
	ds := &ir.DataSource{
		Type:       block.Labels[0],
		Name:       block.Labels[1],
		Properties: make(map[string]*ir.Property),
	}
	for attrName, attr := range block.Body.Attributes {
		val := evalExpr(attr.Expr)
		ds.Properties[attrName] = &ir.Property{
			Name:       attrName,
			IRKey:      attrName,
			Value:      val,
			Type:       inferType(val),
			SourceAttr: attrName,
		}
	}
	return ds
}

func blockToMap(block *hclsyntax.Block) map[string]interface{} {
	m := make(map[string]interface{})
	for name, attr := range block.Body.Attributes {
		m[name] = evalExpr(attr.Expr)
	}
	for _, nested := range block.Body.Blocks {
		m[nested.Type] = blockToMap(nested)
	}
	return m
}

func parseLifecycle(block *hclsyntax.Block, meta *ir.ResourceMeta) {
	for name, attr := range block.Body.Attributes {
		val := evalExpr(attr.Expr)
		switch name {
		case "create_before_destroy":
			if b, ok := val.(bool); ok {
				meta.CreateBeforeDestroy = b
			}
		case "prevent_destroy":
			if b, ok := val.(bool); ok {
				meta.PreventDestroy = b
			}
		case "ignore_changes":
			if list, ok := val.([]interface{}); ok {
				for _, item := range list {
					if s, ok := item.(string); ok {
						meta.LifecycleIgnore = append(meta.LifecycleIgnore, s)
					}
				}
			}
		}
	}
}

func (p *Parser) parseVariableBlock(block *hclsyntax.Block) *ir.Variable {
	v := &ir.Variable{Name: block.Labels[0]}
	for name, attr := range block.Body.Attributes {
		val := evalExpr(attr.Expr)
		switch name {
		case "type":
			if s, ok := val.(string); ok {
				v.Type = s
			}
		case "default":
			v.Default = val
		case "description":
			if s, ok := val.(string); ok {
				v.Description = s
			}
		case "sensitive":
			if b, ok := val.(bool); ok {
				v.Sensitive = b
			}
		}
	}
	return v
}

func (p *Parser) parseOutputBlock(block *hclsyntax.Block) *ir.Output {
	o := &ir.Output{Name: block.Labels[0]}
	for name, attr := range block.Body.Attributes {
		val := evalExpr(attr.Expr)
		switch name {
		case "value":
			o.Value = val
		case "description":
			if s, ok := val.(string); ok {
				o.Description = s
			}
		case "sensitive":
			if b, ok := val.(bool); ok {
				o.Sensitive = b
			}
		}
	}
	return o
}

// evalExpr evaluates an HCL expression into a native Go value.
// For dynamic references (e.g. var.region, aws_vpc.main.id), it preserves
// the traversal path as a "${...}" reference string so the codegen can emit it.
func evalExpr(expr hclsyntax.Expression) interface{} {
	switch e := expr.(type) {
	case *hclsyntax.ScopeTraversalExpr:
		return traversalToRef(e.Traversal)
	case *hclsyntax.RelativeTraversalExpr:
		return traversalToRef(e.Traversal)
	case *hclsyntax.ConditionalExpr:
		return "${conditional}"
	case *hclsyntax.FunctionCallExpr:
		return fmt.Sprintf("${%s(...)}", e.Name)
	default:
		val, diags := expr.Value(nil)
		if diags.HasErrors() || val == cty.NilVal || !val.IsKnown() {
			return "${unknown_expr}"
		}
		return ctyToGo(val)
	}
}

// traversalToRef converts an HCL traversal to a "${resource.name.attr}" reference.
func traversalToRef(t hcl.Traversal) string {
	parts := make([]string, 0, len(t))
	for _, step := range t {
		switch s := step.(type) {
		case hcl.TraverseRoot:
			parts = append(parts, s.Name)
		case hcl.TraverseAttr:
			parts = append(parts, s.Name)
		case hcl.TraverseIndex:
			parts = append(parts, fmt.Sprintf("[%v]", ctyToGo(s.Key)))
		}
	}
	return "${" + strings.Join(parts, ".") + "}"
}

// ctyToGo converts a cty.Value to a native Go type.
func ctyToGo(val cty.Value) interface{} {
	if val.IsNull() || !val.IsKnown() {
		return nil
	}
	ty := val.Type()
	switch ty {
	case cty.String:
		return val.AsString()
	case cty.Number:
		f, _ := val.AsBigFloat().Float64()
		return f
	case cty.Bool:
		return val.True()
	}
	if ty.IsListType() || ty.IsTupleType() || ty.IsSetType() {
		var list []interface{}
		for it := val.ElementIterator(); it.Next(); {
			_, v := it.Element()
			list = append(list, ctyToGo(v))
		}
		return list
	}
	if ty.IsMapType() || ty.IsObjectType() {
		m := make(map[string]interface{})
		for it := val.ElementIterator(); it.Next(); {
			k, v := it.Element()
			m[k.AsString()] = ctyToGo(v)
		}
		return m
	}
	return fmt.Sprintf("%v", val)
}

// inferType returns the PropertyType for a Go-native value.
func inferType(val interface{}) ir.PropertyType {
	switch val.(type) {
	case string:
		return ir.TypeString
	case float64, int, int64:
		return ir.TypeNumber
	case bool:
		return ir.TypeBool
	case []interface{}:
		return ir.TypeList
	case map[string]interface{}:
		return ir.TypeMap
	default:
		return ir.TypeString
	}
}

// classifyResource maps a concrete provider resource type to an IR ResourceClass.
func classifyResource(resourceType string) ir.ResourceClass {
	switch {
	case strings.Contains(resourceType, "instance") && !strings.Contains(resourceType, "db"):
		return ir.ComputeInstance
	case strings.Contains(resourceType, "function") || strings.Contains(resourceType, "lambda"):
		return ir.FunctionCompute
	case strings.Contains(resourceType, "s3_bucket") ||
		strings.Contains(resourceType, "storage_bucket") ||
		strings.Contains(resourceType, "storage_account") ||
		strings.Contains(resourceType, "blob"):
		return ir.StorageBucket
	case strings.Contains(resourceType, "vpc") ||
		strings.Contains(resourceType, "compute_network") ||
		strings.Contains(resourceType, "virtual_network"):
		return ir.NetworkVPC
	case strings.Contains(resourceType, "subnet"):
		return ir.NetworkSubnet
	case strings.Contains(resourceType, "security_group") ||
		strings.Contains(resourceType, "firewall") ||
		strings.Contains(resourceType, "network_security_group"):
		return ir.SecurityGroup
	case strings.Contains(resourceType, "lb") ||
		strings.Contains(resourceType, "load_balancer") ||
		strings.Contains(resourceType, "alb") ||
		strings.Contains(resourceType, "elb"):
		return ir.LoadBalancer
	case strings.Contains(resourceType, "db_instance") ||
		strings.Contains(resourceType, "sql_database") ||
		strings.Contains(resourceType, "sql_server") ||
		strings.Contains(resourceType, "rds"):
		return ir.DatabaseInstance
	case strings.Contains(resourceType, "dns") || strings.Contains(resourceType, "route53"):
		return ir.DNSZone
	case strings.Contains(resourceType, "iam_role") || strings.Contains(resourceType, "role_assignment"):
		return ir.IAMRole
	}
	return ir.UnknownResource
}
