package survey

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to resolve test file path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", "..", ".."))
}

func TestValidateRepoSurveyDocument(t *testing.T) {
	root := repositoryRoot(t)
	surveyPath := filepath.Join(root, "docs", "REPO_SURVEY.md")
	data, err := os.ReadFile(surveyPath)
	if err != nil {
		t.Fatalf("read %s failed: %v", surveyPath, err)
	}
	if err := ValidateRepoSurveyDocument(string(data)); err != nil {
		t.Fatalf("survey document validation failed: %v", err)
	}
}

func TestValidateRepoSurveyDocumentRejectsMissingContent(t *testing.T) {
	content := "## 1. 调研方法与范围\nonly one section"
	if err := ValidateRepoSurveyDocument(content); err == nil {
		t.Fatal("expected validation error for incomplete survey document")
	}
}

func TestMetricsDoNotUseForbiddenJobIDLabel(t *testing.T) {
	root := repositoryRoot(t)
	metricFiles := []string{
		"src/go/internal/api/metrics.go",
		"src/go/internal/scheduler/metrics.go",
		"src/go/internal/worker/metrics.go",
	}

	for _, rel := range metricFiles {
		path := filepath.Join(root, rel)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s failed: %v", rel, err)
		}
		if err := ValidateMetricSourceNoJobIDLabel(string(data), rel); err != nil {
			t.Fatalf("metric label validation failed: %v", err)
		}
	}
}
