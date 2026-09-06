package middleware

import (
	"context"
	"errors"
	"net/http"

	agentapp "github.com/byteBuilderX/stratum/internal/agent/application"
	agentdomain "github.com/byteBuilderX/stratum/internal/agent/domain"
	collabdomain "github.com/byteBuilderX/stratum/internal/collab/domain"
	evalapp "github.com/byteBuilderX/stratum/internal/evaluation/application"
	evaldomain "github.com/byteBuilderX/stratum/internal/evaluation/domain"
	iamapp "github.com/byteBuilderX/stratum/internal/iam/application"
	iamdomain "github.com/byteBuilderX/stratum/internal/iam/domain"
	knowledgedomain "github.com/byteBuilderX/stratum/internal/knowledge/domain"
	llmgatewaydomain "github.com/byteBuilderX/stratum/internal/llmgateway/domain"
	mcpdomain "github.com/byteBuilderX/stratum/internal/mcp/domain"
	memoryapp "github.com/byteBuilderX/stratum/internal/memory/application"
	memorydomain "github.com/byteBuilderX/stratum/internal/memory/domain"
	scheddomain "github.com/byteBuilderX/stratum/internal/scheduler/domain"
	skilldomain "github.com/byteBuilderX/stratum/internal/skill/domain"
	versioningdomain "github.com/byteBuilderX/stratum/internal/versioning/domain"
	workflowdomain "github.com/byteBuilderX/stratum/internal/workflow/domain"
	"github.com/jackc/pgx/v5"
)

// HTTPError carries an explicit HTTP status alongside an error so handlers
// can short-circuit ErrorHandler's sentinel matching for one-off cases
// (validation failures, missing tenant context, etc.).
type HTTPError struct {
	Status int
	Err    error
}

func (e *HTTPError) Error() string {
	if e == nil || e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e *HTTPError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// NewHTTPError wraps err with an explicit HTTP status.
func NewHTTPError(status int, err error) *HTTPError {
	return &HTTPError{Status: status, Err: err}
}

// errorStatusTable maps sentinel domain errors to HTTP status codes.
// New entries only need to be added here; MapErrorToStatus walks the table.
var errorStatusTable = map[error]int{
	context.DeadlineExceeded:                      http.StatusGatewayTimeout,
	llmgatewaydomain.ErrUpstreamRequestFailed:     http.StatusBadGateway,
	agentdomain.ErrEvidenceUnavailable:            http.StatusServiceUnavailable,
	agentdomain.ErrAssistantModelUnavailable:      http.StatusServiceUnavailable,
	agentdomain.ErrSystemPromptNotConfigured:      http.StatusServiceUnavailable,
	agentdomain.ErrCompactionPromptNotConfigured:  http.StatusServiceUnavailable,
	agentdomain.ErrEvidenceInvalid:                http.StatusBadGateway,
	pgx.ErrNoRows:                                 http.StatusNotFound,
	agentdomain.ErrEvidenceNotFound:               http.StatusNotFound,
	knowledgedomain.ErrWorkspaceNotFound:          http.StatusNotFound,
	knowledgedomain.ErrDocumentNotFound:           http.StatusNotFound,
	iamdomain.ErrForbidden:                        http.StatusForbidden,
	iamdomain.ErrMemberNotFound:                   http.StatusNotFound,
	iamdomain.ErrTenantNotFound:                   http.StatusNotFound,
	iamdomain.ErrUserNotFound:                     http.StatusNotFound,
	llmgatewaydomain.ErrModelNotFound:             http.StatusNotFound,
	agentapp.ErrNotFound:                          http.StatusNotFound,
	agentdomain.ErrApprovalNotFound:               http.StatusNotFound,
	agentdomain.ErrApprovalConversationGone:       http.StatusGone,
	agentdomain.ErrApprovalPolicyChanged:          http.StatusConflict,
	agentdomain.ErrProposalNotFound:               http.StatusNotFound,
	agentdomain.ErrNotFound:                       http.StatusNotFound,
	memoryapp.ErrNotFound:                         http.StatusNotFound,
	memorydomain.ErrEntryNotFound:                 http.StatusNotFound,
	memorydomain.ErrSessionNotFound:               http.StatusNotFound,
	memorydomain.ErrFactNotFound:                  http.StatusNotFound,
	memorydomain.ErrEntityNotFound:                http.StatusNotFound,
	memorydomain.ErrInvalidCategory:               http.StatusBadRequest,
	memorydomain.ErrConfidenceOutOfRange:          http.StatusBadRequest,
	memorydomain.ErrImportanceOutOfRange:          http.StatusBadRequest,
	memorydomain.ErrEmptyFactPatch:                http.StatusBadRequest,
	memorydomain.ErrFactNotEditable:               http.StatusConflict,
	memorydomain.ErrSummaryNotFound:               http.StatusNotFound,
	memorydomain.ErrSnapshotNotFound:              http.StatusNotFound,
	memorydomain.ErrSnapshotInvalid:               http.StatusBadRequest,
	memoryapp.ErrMemoryEmbeddingUnavailable:       http.StatusBadGateway,
	skilldomain.ErrSkillNotFound:                  http.StatusNotFound,
	skilldomain.ErrSkillDraftStale:                http.StatusConflict,
	mcpdomain.ErrServerNotFound:                   http.StatusNotFound,
	evalapp.ErrSuiteNotFound:                      http.StatusNotFound,
	evalapp.ErrJobNotFound:                        http.StatusNotFound,
	evalapp.ErrRunNotFound:                        http.StatusNotFound,
	evalapp.ErrExperimentNotFound:                 http.StatusNotFound,
	evaldomain.ErrCenterResourceNotFound:          http.StatusNotFound,
	evaldomain.ErrCandidateNotFound:               http.StatusNotFound,
	workflowdomain.ErrNotFound:                    http.StatusNotFound,
	agentapp.ErrApprovalExpired:                   http.StatusGone,
	agentdomain.ErrApprovalRoleDenied:             http.StatusForbidden,
	agentdomain.ErrApprovalSelfDecision:           http.StatusConflict,
	agentdomain.ErrApprovalAssigneeInvalid:        http.StatusBadRequest,
	agentdomain.ErrApprovalInvalidated:            http.StatusConflict,
	agentdomain.ErrTooManyPendingApprovals:        http.StatusTooManyRequests,
	knowledgedomain.ErrWorkspaceConflict:          http.StatusConflict,
	agentdomain.ErrProposalStale:                  http.StatusConflict,
	agentdomain.ErrProposalExpired:                http.StatusConflict,
	agentdomain.ErrProposalAlreadyClaimed:         http.StatusConflict,
	agentdomain.ErrProposalUnknownOutcome:         http.StatusConflict,
	agentdomain.ErrForbidden:                      http.StatusForbidden,
	skilldomain.ErrForbidden:                      http.StatusForbidden,
	mcpdomain.ErrForbidden:                        http.StatusForbidden,
	knowledgedomain.ErrForbidden:                  http.StatusForbidden,
	knowledgedomain.ErrWorkspaceLinked:            http.StatusConflict,
	knowledgedomain.ErrDuplicateDocument:          http.StatusConflict,
	knowledgedomain.ErrDocumentProcessing:         http.StatusConflict,
	agentapp.ErrNameConflict:                      http.StatusConflict,
	mcpdomain.ErrNameConflict:                     http.StatusConflict,
	skilldomain.ErrSkillNameConflict:              http.StatusConflict,
	skilldomain.ErrSkillDraftNotFound:             http.StatusConflict,
	skilldomain.ErrSkillLinked:                    http.StatusConflict,
	memorydomain.ErrFactQuotaExceeded:             http.StatusConflict,
	memorydomain.ErrFactAlreadyDeleted:            http.StatusConflict,
	evaldomain.ErrOptimizationIdempotencyConflict: http.StatusConflict,
	evaldomain.ErrFeedbackIdempotencyConflict:     http.StatusConflict,
	evalapp.ErrSuiteDraftMissing:                  http.StatusConflict,
	agentapp.ErrApprovalNotApproved:               http.StatusConflict,
	agentapp.ErrApprovalOutcomeUnknown:            http.StatusConflict,
	agentdomain.ErrApprovalAlreadyDecided:         http.StatusConflict,
	agentdomain.ErrApprovalAlreadyExecuted:        http.StatusConflict,
	workflowdomain.ErrRevisionConflict:            http.StatusConflict,
	workflowdomain.ErrIdempotencyConflict:         http.StatusConflict,
	workflowdomain.ErrInvalidTransition:           http.StatusConflict,
	workflowdomain.ErrGenerationConflict:          http.StatusConflict,
	workflowdomain.ErrFenceConflict:               http.StatusConflict,
	workflowdomain.ErrDecisionConflict:            http.StatusConflict,
	workflowdomain.ErrApprovalRequired:            http.StatusConflict,
	evaldomain.ErrExperimentStateConflict:         http.StatusConflict,
	evaldomain.ErrExperimentCommandConflict:       http.StatusConflict,
	evaldomain.ErrExperimentDeploymentConflict:    http.StatusConflict,
	evaldomain.ErrExperimentStableNotPublished:    http.StatusConflict,
	evaldomain.ErrExperimentInvalidCandidate:      http.StatusConflict,
	evaldomain.ErrExperimentSuiteNotPublished:     http.StatusConflict,
	evaldomain.ErrExperimentOfflineRunRequired:    http.StatusConflict,
	evaldomain.ErrExperimentCommandNotAllowed:     http.StatusConflict,
	evaldomain.ErrCandidateStateConflict:          http.StatusConflict,
	evaldomain.ErrEntityReferenced:                http.StatusConflict,
	evaldomain.ErrCandidateCommandConflict:        http.StatusConflict,
	evaldomain.ErrCandidateCommandNotAllowed:      http.StatusConflict,
	evaldomain.ErrFeedbackTraceForbidden:          http.StatusForbidden,
	evaldomain.ErrDeleteForbidden:                 http.StatusForbidden,
	agentapp.ErrInvalidSkill:                      http.StatusUnprocessableEntity,
	agentdomain.ErrProposalApplyFailed:            http.StatusUnprocessableEntity,
	skilldomain.ErrConcurrencyLimit:               http.StatusTooManyRequests,
	knowledgedomain.ErrIngestQueueFull:            http.StatusTooManyRequests,
	iamapp.ErrForbiddenAdminOrOwner:               http.StatusForbidden,
	agentdomain.ErrProposalForbidden:              http.StatusForbidden,
	agentdomain.ErrOperationProposalNotFound:      http.StatusNotFound,
	agentdomain.ErrOperationProposalResolved:      http.StatusConflict,
	agentdomain.ErrOperationProposalPending:       http.StatusConflict,
	agentdomain.ErrOperationProposalExpired:       http.StatusConflict,
	collabdomain.ErrCollabForbidden:               http.StatusForbidden,
	collabdomain.ErrCollabNotFound:                http.StatusNotFound,
	collabdomain.ErrCollabInvalidTransition:       http.StatusConflict,
	collabdomain.ErrCollabInvalidInput:            http.StatusBadRequest,
	collabdomain.ErrCollabConflict:                http.StatusConflict,
	scheddomain.ErrScheduledTaskForbidden:         http.StatusForbidden,
	scheddomain.ErrScheduledTaskNotFound:          http.StatusNotFound,
	scheddomain.ErrScheduledTaskInvalidInput:      http.StatusBadRequest,
	scheddomain.ErrScheduledTaskInvalidCron:       http.StatusBadRequest,
	iamapp.ErrForbiddenOwner:                      http.StatusForbidden,
	iamapp.ErrForbiddenSelfModify:                 http.StatusForbidden,
	iamapp.ErrForbiddenOwnerRole:                  http.StatusForbidden,
	iamapp.ErrForbiddenRemoveOwner:                http.StatusForbidden,
	iamapp.ErrForbiddenAdminRemove:                http.StatusForbidden,
	memorydomain.ErrAgentMemoryDisabled:           http.StatusForbidden,
	memorydomain.ErrScopeMismatch:                 http.StatusForbidden,
	workflowdomain.ErrForbidden:                   http.StatusForbidden,
	iamapp.ErrInvalidSettings:                     http.StatusBadRequest,
	agentdomain.ErrProposalInvalid:                http.StatusBadRequest,
	agentdomain.ErrInvalidAgentModel:              http.StatusBadRequest,
	agentdomain.ErrInvalidSamplingParameters:      http.StatusBadRequest,
	agentdomain.ErrInvalidMaxIterations:           http.StatusBadRequest,
	iamdomain.ErrDefaultTenantDelete:              http.StatusBadRequest,
	iamdomain.ErrUsernameTaken:                    http.StatusConflict,
	knowledgedomain.ErrInvalidEmbeddingModel:      http.StatusBadRequest,
	knowledgedomain.ErrEmbeddingModelRequired:     http.StatusBadRequest,
	llmgatewaydomain.ErrModelNotEmbeddingEnabled:  http.StatusBadRequest,
	llmgatewaydomain.ErrInvalidFallbackCandidates: http.StatusBadRequest,
	llmgatewaydomain.ErrInvalidModelInput:         http.StatusBadRequest,
	llmgatewaydomain.ErrSamplingOutOfRange:        http.StatusBadRequest,
	llmgatewaydomain.ErrCapabilityUnsupported:     http.StatusBadRequest,
	knowledgedomain.ErrInvalidQueryMode:           http.StatusBadRequest,
	knowledgedomain.ErrInvalidRerankIdentity:      http.StatusBadRequest,
	knowledgedomain.ErrInvalidScoreThreshold:      http.StatusBadRequest,
	knowledgedomain.ErrRerankModelRequired:        http.StatusBadRequest,
	knowledgedomain.ErrInvalidRerankModel:         http.StatusBadRequest,
	knowledgedomain.ErrInvalidTopK:                http.StatusBadRequest,
	knowledgedomain.ErrInvalidRerankTopK:          http.StatusBadRequest,
	knowledgedomain.ErrInvalidJudgeModel:          http.StatusBadRequest,
	knowledgedomain.ErrEmbeddingModelImmutable:    http.StatusBadRequest,
	knowledgedomain.ErrChunkSizeImmutable:         http.StatusBadRequest,
	knowledgedomain.ErrChunkOverlapImmutable:      http.StatusBadRequest,
	knowledgedomain.ErrChunkLimitExceeded:         http.StatusBadRequest,
	skilldomain.ErrSkillTypeImmutable:             http.StatusBadRequest,
	skilldomain.ErrNotCodeSkill:                   http.StatusBadRequest,
	skilldomain.ErrSkillUnsupportedType:           http.StatusBadRequest,
	skilldomain.ErrSkillCodeAnalysis:              http.StatusBadRequest,
	skilldomain.ErrSkillNotPublishable:            http.StatusBadRequest,
	evalapp.ErrSuiteNameRequired:                  http.StatusBadRequest,
	evalapp.ErrSuiteCasesRequired:                 http.StatusBadRequest,
	evalapp.ErrSuiteCaseInputRequired:             http.StatusBadRequest,
	evalapp.ErrSuiteCaseInvalidScript:             http.StatusBadRequest,
	evaldomain.ErrInvalidCenterQuery:              http.StatusBadRequest,
	evaldomain.ErrInvalidCandidateCommand:         http.StatusBadRequest,
	memorydomain.ErrInvalidStatus:                 http.StatusBadRequest,
	memorydomain.ErrUserIDMismatch:                http.StatusBadRequest,
	memorydomain.ErrEmptyContent:                  http.StatusBadRequest,
	memorydomain.ErrMigrationInvalidTenant:        http.StatusBadRequest,
	memorydomain.ErrMigrationEmptyModel:           http.StatusBadRequest,
	memorydomain.ErrMigrationSameModel:            http.StatusBadRequest,
	memorydomain.ErrMigrationUnknownModel:         http.StatusBadRequest,
	memorydomain.ErrMigrationNotFound:             http.StatusNotFound,
	memorydomain.ErrMigrationAlreadyActive:        http.StatusConflict,
	memorydomain.ErrMigrationNotActive:            http.StatusConflict,
	memorydomain.ErrMigrationProgressRegressed:    http.StatusConflict,
	memorydomain.ErrMigrationNotRetryable:         http.StatusConflict,
	workflowdomain.ErrInvalidSpec:                 http.StatusBadRequest,
	workflowdomain.ErrInvalidInputSchema:          http.StatusBadRequest,
	mcpdomain.ErrUnsupportedTransport:             http.StatusBadRequest,
	mcpdomain.ErrInvalidServerURL:                 http.StatusBadRequest,
	mcpdomain.ErrUnsupportedAuth:                  http.StatusBadRequest,
	mcpdomain.ErrSessionMissing:                   http.StatusNotFound,
	mcpdomain.ErrTransportFailed:                  http.StatusBadGateway,
	versioningdomain.ErrVersionNotFound:           http.StatusNotFound,
}

// MapErrorToStatus walks the wrap chain and returns the HTTP status that
// should be sent for err. Handlers that emit `c.Error(err)` must rely on
// this table — no scattered `errors.Is` switch blocks elsewhere.
//
// Mapping policy:
//   - NotFound family    → 404
//   - Conflict / dup     → 409
//   - Forbidden family   → 403
//   - Unauthorized       → 401
//   - Validation / 4xx   → 400
//   - Concurrency limit  → 429
//   - Unprocessable      → 422
//   - default            → 500
func MapErrorToStatus(err error) int {
	if err == nil {
		return http.StatusOK
	}

	var he *HTTPError
	if errors.As(err, &he) && he.Status != 0 {
		return he.Status
	}

	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		return http.StatusRequestEntityTooLarge
	}

	// L2 上下文窗口拦截：contextLengthExceededError 在 infrastructure 包（带
	// ContextLengthExceeded() 方法），不跨包注册 table 条目，鸭子类型探测。
	var ctxLen interface{ ContextLengthExceeded() bool }
	if errors.As(err, &ctxLen) && ctxLen.ContextLengthExceeded() {
		return http.StatusBadRequest
	}

	for sentinel, status := range errorStatusTable {
		if errors.Is(err, sentinel) {
			return status
		}
	}

	return http.StatusInternalServerError
}
