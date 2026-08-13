package modelaudit

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditModCatalogReloadCreatesContentAddressedSnapshot(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "mods")
	cache := filepath.Join(root, "cache")
	modDirectory := filepath.Join(source, "random-number")
	writeAuditModFixture(t, modDirectory, auditModLLMFixture("random-number"))

	catalog, err := NewAuditModCatalog(source, cache)
	if err != nil {
		t.Fatalf("NewAuditModCatalog() error = %v", err)
	}
	first, err := catalog.Reload(context.Background())
	if err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	if len(first.Mods) != 1 || len(first.Issues) != 0 {
		t.Fatalf("Reload() = %#v, want one valid mod", first)
	}
	firstReference := first.Mods[0].Reference
	if err := firstReference.Validate(); err != nil {
		t.Fatalf("reference Validate() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(first.Mods[0].SnapshotPath, ".snapshot.json")); err != nil {
		t.Fatalf("snapshot marker error = %v", err)
	}

	if err := os.WriteFile(filepath.Join(modDirectory, "probe-prompt.md"), []byte("Return one integer from 1 to 10."), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	second, err := catalog.Reload(context.Background())
	if err != nil {
		t.Fatalf("second Reload() error = %v", err)
	}
	if second.Mods[0].Reference.ContentSHA256 == firstReference.ContentSHA256 {
		t.Fatal("content SHA did not change after prompt edit")
	}
	if _, err := os.Stat(filepath.Join(cache, firstReference.ContentSHA256, ".snapshot.json")); err != nil {
		t.Fatalf("old snapshot was not preserved: %v", err)
	}
	if _, found := catalog.Get(firstReference); found {
		t.Fatal("Get() returned an old reference as the current mod")
	}
}

func TestAuditModCatalogIsolatesInvalidAndDuplicateDirectories(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "mods")
	writeAuditModFixture(t, filepath.Join(source, "first"), auditModLLMFixture("duplicate-mod"))
	writeAuditModFixture(t, filepath.Join(source, "second"), auditModLLMFixture("duplicate-mod"))
	if err := os.MkdirAll(filepath.Join(source, "missing-manifest"), 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	catalog, err := NewAuditModCatalog(source, filepath.Join(root, "cache"))
	if err != nil {
		t.Fatalf("NewAuditModCatalog() error = %v", err)
	}
	snapshot, err := catalog.Reload(context.Background())
	if err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	if len(snapshot.Mods) != 0 {
		t.Fatalf("Reload() mods = %#v, want duplicate mods excluded", snapshot.Mods)
	}
	if len(snapshot.Issues) != 3 {
		t.Fatalf("Reload() issues = %#v, want three directory issues", snapshot.Issues)
	}
}

func TestAuditModManifestSupportsThreeAnalysisModes(t *testing.T) {
	tests := []struct {
		name     string
		analysis AuditModAnalysis
	}{
		{
			name: "llm",
			analysis: AuditModAnalysis{Mode: AuditModAnalysisLLM, PromptFile: "analysis-prompt.md",
				MethodFile: "analysis-method.md", ResultSchemaFile: "analysis-result.schema.json"},
		},
		{
			name: "python",
			analysis: AuditModAnalysis{Mode: AuditModAnalysisPython, MethodFile: "analysis-method.md",
				ResultSchemaFile: "analysis-result.schema.json", Python: &AuditModPythonAnalysis{Entry: "analysis.py", TimeoutSeconds: 60}},
		},
		{
			name: "manual",
			analysis: AuditModAnalysis{Mode: AuditModAnalysisManual, MethodFile: "analysis-method.md",
				ResultSchemaFile: "analysis-result.schema.json", Manual: &AuditModManualAnalysis{Runtime: "java", CommandExample: "java -jar analyzer.jar"}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := auditModLLMFixture("analysis-" + test.name)
			manifest.Analysis = test.analysis
			if err := manifest.Validate(); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestDecodeAuditModManifestRejectsTrailingJSON(t *testing.T) {
	encoded, err := json.Marshal(auditModLLMFixture("trailing-json"))
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if _, err := decodeAuditModManifest(append(encoded, []byte(` {"unexpected":true}`)...)); err == nil {
		t.Fatal("decodeAuditModManifest() accepted a second JSON object")
	}
}

func auditModLLMFixture(id string) AuditModManifest {
	minimum := 1.0
	maximum := 100.0
	return AuditModManifest{
		SchemaVersion: AuditModSchemaVersion,
		ID:            id,
		Version:       "1.0.0",
		Name:          "Random number",
		Description:   "Collect a number distribution.",
		ProbeFile:     "probe-prompt.md",
		RulesFile:     "rules.json",
		Sampling: AuditModSampling{
			Mode: AuditModSamplingIndependent, DefaultSampleCount: 10, MaximumSamples: 1000, MaxOutputTokens: 16,
		},
		Parser: AuditModParser{Type: AuditModParserNumber, Minimum: &minimum, Maximum: &maximum},
		Aggregation: AuditModAggregation{Type: AuditModAggregationHistogram, Buckets: []AuditModHistogramBucket{
			{ID: "lower", Minimum: 1, Maximum: 50}, {ID: "upper", Minimum: 51, Maximum: 100},
		}},
		Analysis: AuditModAnalysis{
			Mode: AuditModAnalysisLLM, PromptFile: "analysis-prompt.md", MethodFile: "analysis-method.md",
			ResultSchemaFile: "analysis-result.schema.json",
		},
	}
}

func writeAuditModFixture(t *testing.T, directory string, manifest AuditModManifest) {
	t.Helper()
	if err := os.MkdirAll(directory, 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	files := map[string]string{
		"manifest.json":               string(encoded),
		"probe-prompt.md":             "Return one integer.",
		"rules.json":                  `{"rules":[],"fallback":{"verdict":"inconclusive","confidence":0}}`,
		"analysis-prompt.md":          "Analyze {{analysis_data_json}}.",
		"analysis-method.md":          "# Method\n\nAnalyze the current run only.",
		"analysis-result.schema.json": `{"type":"object"}`,
		"analysis.py":                 "print('{}')",
	}
	for path, content := range files {
		if !auditModFixtureReferencesFile(manifest, path) && path != auditModManifestFile {
			continue
		}
		target := filepath.Join(directory, filepath.FromSlash(path))
		if err := os.WriteFile(target, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", target, err)
		}
	}
}

func auditModFixtureReferencesFile(manifest AuditModManifest, path string) bool {
	for _, referenced := range manifest.referencedFiles() {
		if strings.EqualFold(referenced, path) {
			return true
		}
	}
	return false
}
