package ir

import (
	"math"
	"testing"
)

func TestComputeCompositeUsesTheDocumentedWeights(t *testing.T) {
	// README and docs/architecture.md both state
	// composite = 0.40*coverage + 0.35*validity + 0.25*semantics.
	score := TranslationScore{CoverageRatio: 1, ValidityRatio: 0, SemanticRatio: 0}
	score.ComputeComposite()
	if math.Abs(score.Composite-0.40) > 1e-9 {
		t.Fatalf("coverage weight = %v, want 0.40", score.Composite)
	}

	score = TranslationScore{CoverageRatio: 0, ValidityRatio: 1, SemanticRatio: 0}
	score.ComputeComposite()
	if math.Abs(score.Composite-0.35) > 1e-9 {
		t.Fatalf("validity weight = %v, want 0.35", score.Composite)
	}

	score = TranslationScore{CoverageRatio: 0, ValidityRatio: 0, SemanticRatio: 1}
	score.ComputeComposite()
	if math.Abs(score.Composite-0.25) > 1e-9 {
		t.Fatalf("semantic weight = %v, want 0.25", score.Composite)
	}

	score = TranslationScore{CoverageRatio: 1, ValidityRatio: 1, SemanticRatio: 1}
	score.ComputeComposite()
	if math.Abs(score.Composite-1.0) > 1e-9 {
		t.Fatalf("a perfect translation must score 1.0, got %v", score.Composite)
	}
}

func TestRewriteReturnsUnknownReferencesUnchanged(t *testing.T) {
	graph := NewRefGraph()
	if got := graph.Rewrite("aws_vpc.main.id"); got != "aws_vpc.main.id" {
		t.Fatalf("an unmapped reference must survive intact, got %q", got)
	}
}

func TestRewritePrefersTheAttributeLevelMapping(t *testing.T) {
	graph := NewRefGraph()
	graph.Register("aws_vpc", "main", "google_compute_network", "main")
	graph.RegisterAttr("aws_vpc.main.id", "google_compute_network.main.name")

	// The attribute mapping is more specific than the resource-level rule,
	// which would otherwise turn ".id" into ".self_link".
	if got := graph.Rewrite("aws_vpc.main.id"); got != "google_compute_network.main.name" {
		t.Fatalf("got %q, want the registered attribute mapping", got)
	}
}

func TestRewriteMapsCommonAttributeSuffixes(t *testing.T) {
	graph := NewRefGraph()
	graph.Register("aws_instance", "web", "google_compute_instance", "web")

	cases := map[string]string{
		"aws_instance.web.id":         "google_compute_instance.web.self_link",
		"aws_instance.web.arn":        "google_compute_instance.web.id",
		"aws_instance.web.public_ip":  "google_compute_instance.web.network_interface.0.access_config.0.nat_ip",
		"aws_instance.web.private_ip": "google_compute_instance.web.network_interface.0.network_ip",
		"aws_instance.web.tags":       "google_compute_instance.web.tags",
	}
	for input, want := range cases {
		if got := graph.Rewrite(input); got != want {
			t.Errorf("Rewrite(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRewriteRequiresAFullSegmentMatch(t *testing.T) {
	graph := NewRefGraph()
	graph.Register("aws_vpc", "main", "google_compute_network", "main")

	// "aws_vpc.mainline" shares a prefix with "aws_vpc.main" but is a different
	// resource, so it must not be rewritten.
	if got := graph.Rewrite("aws_vpc.mainline.id"); got != "aws_vpc.mainline.id" {
		t.Fatalf("prefix collision rewrote an unrelated resource: %q", got)
	}
	// The bare resource address with no attribute is also not a match.
	if got := graph.Rewrite("aws_vpc.main"); got != "aws_vpc.main" {
		t.Fatalf("bare address should be left alone, got %q", got)
	}
}
