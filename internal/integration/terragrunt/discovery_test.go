package terragrunt

import (
	"os"
	"path/filepath"
	"testing"
)

func exampleStackRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "..", "example", "terragrunt"))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestDiscoverUnitsResolvesLocalSources(t *testing.T) {
	root := exampleStackRoot(t)
	units, err := DiscoverUnits(root, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 2 {
		t.Fatalf("found %d units, want 2", len(units))
	}
	wantModule := filepath.Join(root, "modules", "storage")
	for _, unit := range units {
		if unit.Unresolved || unit.Remote {
			t.Fatalf("unit was not resolved locally: %+v", unit)
		}
		if unit.ModulePath != wantModule {
			t.Errorf("module path = %q, want %q", unit.ModulePath, wantModule)
		}
	}
}

func TestDiscoverUnitsMarksDynamicSource(t *testing.T) {
	root := t.TempDir()
	config := `locals { module_path = "../module" }
terraform { source = local.module_path }
`
	if err := os.WriteFile(filepath.Join(root, "terragrunt.hcl"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	units, err := DiscoverUnits(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 1 || !units[0].Unresolved {
		t.Fatalf("dynamic source should be unresolved: %+v", units)
	}
}

func TestTranslateStackDeduplicatesSharedModules(t *testing.T) {
	report, reportPath, err := TranslateStack(StackConfig{
		Root:          exampleStackRoot(t),
		OutputRoot:    t.TempDir(),
		From:          "auto",
		To:            "google",
		Kp:            0.8,
		Ki:            0.1,
		Kd:            0.05,
		MaxIter:       8,
		MinAccuracy:   0.5,
		FailOnSkipped: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.UniqueModules != 1 || report.Summary.TranslatedModules != 1 {
		t.Fatalf("unexpected summary: %+v", report.Summary)
	}
	if report.Summary.Units != 2 {
		t.Fatalf("units = %d, want 2", report.Summary.Units)
	}
	if _, err := os.Stat(reportPath); err != nil {
		t.Fatalf("stack report missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(report.OutputRoot, "modules", "storage", "main.tf")); err != nil {
		t.Fatalf("translated shared module missing: %v", err)
	}
}
