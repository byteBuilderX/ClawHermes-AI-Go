package middleware

import (
	"net/http"
	"testing"

	evalapp "github.com/byteBuilderX/stratum/internal/evaluation/application"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
)

func TestMapEvaluationEvolutionErrors(t *testing.T) {
	tests := []struct {
		err  error
		want int
	}{
		{domain.ErrInvalidCenterQuery, http.StatusBadRequest},
		{domain.ErrInvalidCandidateCommand, http.StatusBadRequest},
		{domain.ErrCenterResourceNotFound, http.StatusNotFound},
		{domain.ErrCandidateNotFound, http.StatusNotFound},
		{domain.ErrExperimentStateConflict, http.StatusConflict},
		{domain.ErrExperimentCommandConflict, http.StatusConflict},
		{domain.ErrExperimentDeploymentConflict, http.StatusConflict},
		{domain.ErrExperimentStableNotPublished, http.StatusConflict},
		{domain.ErrExperimentInvalidCandidate, http.StatusConflict},
		{domain.ErrExperimentSuiteNotPublished, http.StatusConflict},
		{domain.ErrExperimentOfflineRunRequired, http.StatusConflict},
		{domain.ErrCandidateStateConflict, http.StatusConflict},
		{domain.ErrOptimizationIdempotencyConflict, http.StatusConflict},
		{domain.ErrFeedbackIdempotencyConflict, http.StatusConflict},
		{domain.ErrFeedbackTraceForbidden, http.StatusForbidden},
		// ErrSuiteDraftMissing（草稿缺失 409）：suite 编排错误归 application 哨兵，
		// 单独列出以免与 domain 哨兵混淆。
		{evalapp.ErrSuiteDraftMissing, http.StatusConflict},
	}
	for _, tc := range tests {
		if got := MapErrorToStatus(tc.err); got != tc.want {
			t.Errorf("MapErrorToStatus(%v)=%d want %d", tc.err, got, tc.want)
		}
	}
}
