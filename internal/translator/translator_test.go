package translator

import (
	"testing"

	"github.com/terra-translate/internal/ir"
)

func TestFanOutCoverageNeverExceedsOne(t *testing.T) {
	mappings, err := LoadMappings("aws", "google", "")
	if err != nil {
		t.Fatal(err)
	}
	properties := map[string]*ir.Property{}
	for _, name := range []string{
		"identifier", "engine", "engine_version", "instance_class", "allocated_storage",
		"username", "password", "multi_az", "backup_retention_period", "skip_final_snapshot", "tags",
	} {
		properties[name] = &ir.Property{Name: name, SourceAttr: name, Value: "value"}
	}
	module := &ir.Module{Resources: []*ir.Resource{{
		OriginalType: "aws_db_instance",
		Name:         "database",
		Properties:   properties,
	}}}

	result := New(mappings).Translate(module, 0.5)
	if result.Accuracy > 1 {
		t.Fatalf("overall coverage = %f, want <= 1", result.Accuracy)
	}
	if got := result.ResourceAccuracies[0].Score; got > 1 {
		t.Fatalf("resource coverage = %f, want <= 1", got)
	}
}
