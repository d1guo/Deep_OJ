package survey

import (
	"fmt"
	"strings"
)

// RequiredRepoSurveySections documents the minimum chapter structure of docs/REPO_SURVEY.md.
var RequiredRepoSurveySections = []string{
	"## 1. 调研方法与范围",
	"## 2. 队列实现现状",
	"## 3. PostgreSQL Schema 现状",
	"## 4. Worker 执行链路现状",
	"## 5. 可观测性现状",
}

// RequiredRepoSurveyTokens ensures the survey keeps concrete keys, commands and code paths.
var RequiredRepoSurveyTokens = []string{
	"queue:pending",
	"queue:processing",
	"stream:results",
	"sql/migrations/001_init.sql",
	"src/go/internal/repository/postgres.go",
	"src/go/cmd/scheduler/main.go",
	"src/go/internal/scheduler/ack_listener.go",
	"src/go/internal/worker/judge.go",
	"src/go/internal/api/metrics.go",
	"scripts/repo_survey_probe.sh",
}

// ValidateRepoSurveyDocument checks whether docs/REPO_SURVEY.md has the expected minimum content.
func ValidateRepoSurveyDocument(content string) error {
	missing := make([]string, 0)
	for _, section := range RequiredRepoSurveySections {
		if !strings.Contains(content, section) {
			missing = append(missing, section)
		}
	}
	for _, token := range RequiredRepoSurveyTokens {
		if !strings.Contains(content, token) {
			missing = append(missing, token)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("repo survey document missing %d required items: %s", len(missing), strings.Join(missing, ", "))
}

// ValidateMetricSourceNoJobIDLabel blocks forbidden high-cardinality labels in metric definitions.
func ValidateMetricSourceNoJobIDLabel(source string, filePath string) error {
	if strings.Contains(source, "\"job_id\"") {
		return fmt.Errorf("%s contains forbidden metric label job_id", filePath)
	}
	return nil
}
