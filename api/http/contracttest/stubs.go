// Package contracttest is the single source of truth for the golden HTTP
// contract harness: the domain-port stubs (canonical, copied verbatim from
// api/http/contract_test.go), fixtures, and the wiring.Container assembly
// in container.go. Consumers are the goldens recorder
// (scripts/record-contracts.go) and the golden verifier
// (api/http/contract_test.go).
package contracttest

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"

	agentdomain "github.com/byteBuilderX/stratum/internal/agent/domain"
	agentport "github.com/byteBuilderX/stratum/internal/agent/domain/port"
	auditdomain "github.com/byteBuilderX/stratum/internal/audit/domain"
	auditport "github.com/byteBuilderX/stratum/internal/audit/domain/port"
	iamdomain "github.com/byteBuilderX/stratum/internal/iam/domain"
	iamport "github.com/byteBuilderX/stratum/internal/iam/domain/port"
	llmdomain "github.com/byteBuilderX/stratum/internal/llmgateway/domain"
	llmport "github.com/byteBuilderX/stratum/internal/llmgateway/domain/port"
	platformdomain "github.com/byteBuilderX/stratum/internal/platform/domain"
	schedapp "github.com/byteBuilderX/stratum/internal/scheduler/application"
	scheddomain "github.com/byteBuilderX/stratum/internal/scheduler/domain"
	schedport "github.com/byteBuilderX/stratum/internal/scheduler/domain/port"
	versioningdomain "github.com/byteBuilderX/stratum/internal/versioning/domain"
	workflowdomain "github.com/byteBuilderX/stratum/internal/workflow/domain"
	workflowport "github.com/byteBuilderX/stratum/internal/workflow/domain/port"

	"github.com/byteBuilderX/stratum/pkg/observability"
)

// ── Stub repositories ──────────────────────────────────────────────────────

var errStubNotFound = errors.New("stub: not found")

type contractProviderRepo struct{}

func (contractProviderRepo) Create(_ context.Context, _ *llmdomain.Provider) error {
	return nil
}
func (contractProviderRepo) Get(_ context.Context, _ string) (*llmdomain.Provider, error) {
	return &llmdomain.Provider{
		ID: "contract-provider", Name: "stub", Kind: llmdomain.ProviderOpenAICompat,
		BaseURL: "https://stub.example.com/v1", Enabled: true,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}, nil
}
func (contractProviderRepo) GetMeta(_ context.Context, _ string) (*llmdomain.Provider, error) {
	return &llmdomain.Provider{
		ID: "contract-provider", Name: "stub", Kind: llmdomain.ProviderOpenAICompat,
		BaseURL: "https://stub.example.com/v1", Enabled: true,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}, nil
}
func (contractProviderRepo) List(_ context.Context) ([]llmdomain.Provider, error) {
	return nil, nil
}
func (contractProviderRepo) Update(_ context.Context, _ *llmdomain.Provider, _ string, _ *auditdomain.ResourceChangeAuditEvent) error {
	return nil
}
func (contractProviderRepo) Delete(_ context.Context, _ string) error { return nil }

type contractModelRepo struct{}

func (contractModelRepo) Create(_ context.Context, _ *llmdomain.Model) error { return nil }
func (contractModelRepo) Get(_ context.Context, _ string) (*llmdomain.Model, error) {
	return nil, errStubNotFound
}
func (contractModelRepo) List(_ context.Context, _ llmport.ModelFilter) ([]llmdomain.Model, error) {
	return nil, nil
}
func (contractModelRepo) Update(_ context.Context, _ *llmdomain.Model, _ string, _ *auditdomain.ResourceChangeAuditEvent) error {
	return nil
}
func (contractModelRepo) UpsertDiscovered(_ context.Context, _ string, _ []llmdomain.Model) ([]llmdomain.Model, error) {
	return nil, nil
}
func (contractModelRepo) Delete(_ context.Context, _ string) error         { return nil }
func (contractModelRepo) Toggle(_ context.Context, _ string, _ bool) error { return nil }

type contractProviderRuntime struct{}

func (contractProviderRuntime) ListModels(_ context.Context, _ llmdomain.Provider) ([]llmport.DiscoveredModel, error) {
	return []llmport.DiscoveredModel{{Name: "mock-model-1"}, {Name: "mock-model-2"}}, nil
}
func (contractProviderRuntime) Health(_ context.Context, _ llmdomain.Provider) error { return nil }

// ── Workflow stubs ─────────────────────────────────────────────────────────

type contractDefRepo struct{}

func (contractDefRepo) CreateDefinition(_ context.Context, _ string, _ *workflowdomain.Definition, _ *auditdomain.ResourceChangeAuditEvent) error {
	return nil
}
func (contractDefRepo) GetDefinition(_ context.Context, _ string, _ string) (*workflowdomain.Definition, error) {
	return &workflowdomain.Definition{
		ID: "contract-def", Name: "stub-workflow", Description: "contract stub",
		Revision: 1, Spec: workflowdomain.Spec{Nodes: []workflowdomain.Node{}, Edges: []workflowdomain.Edge{}},
		InputSchema: workflowdomain.InputSchema{Fields: []workflowdomain.InputField{}},
		CreatedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}, nil
}
func (contractDefRepo) UpdateDefinition(_ context.Context, _ string, _ *workflowdomain.Definition, _ int64, _ string, _ *auditdomain.ResourceChangeAuditEvent) error {
	return nil
}
func (contractDefRepo) DeleteDefinition(_ context.Context, _ string, _ string, _ *auditdomain.ResourceChangeAuditEvent) error {
	return nil
}
func (contractDefRepo) ListDefinitions(_ context.Context, _ string, _ workflowport.DefinitionListQuery) ([]workflowdomain.Definition, int, error) {
	return nil, 0, nil
}

type contractVersionRepo struct{}

func (contractVersionRepo) CreateVersion(_ context.Context, _ string, _ *workflowdomain.Version, _ *auditdomain.ResourceChangeAuditEvent) error {
	return nil
}
func (contractVersionRepo) GetVersion(_ context.Context, _ string, _ string) (*workflowdomain.Version, error) {
	return nil, workflowdomain.ErrNotFound
}
func (contractVersionRepo) NextVersionNumber(_ context.Context, _ string, _ string) (int64, error) {
	return 1, nil
}
func (contractVersionRepo) SetActiveVersion(_ context.Context, _ string, _ string, _ string, _ *auditdomain.ResourceChangeAuditEvent) error {
	return nil
}
func (contractVersionRepo) ListVersions(_ context.Context, _ string, _ string, _ workflowport.VersionListQuery) ([]workflowdomain.Version, int, error) {
	return nil, 0, nil
}

type contractRunStore struct{}

func (contractRunStore) FindRunByIdempotency(_ context.Context, _ string, _ string) (*workflowdomain.Run, error) {
	return nil, workflowdomain.ErrNotFound
}
func (contractRunStore) CreateRun(_ context.Context, _ string, _ *workflowdomain.Run) error {
	return nil
}
func (contractRunStore) GetRun(_ context.Context, _ string, _ string) (*workflowdomain.Run, error) {
	return nil, workflowdomain.ErrNotFound
}
func (contractRunStore) UpdateRun(_ context.Context, _ string, _ *workflowdomain.Run) error {
	return nil
}
func (contractRunStore) SaveAttempt(_ context.Context, _ string, _ workflowdomain.NodeAttempt) error {
	return nil
}
func (contractRunStore) ListAttempts(_ context.Context, _ string, _ string) ([]workflowdomain.NodeAttempt, error) {
	return nil, nil
}
func (contractRunStore) ListRuns(_ context.Context, _ string, _ workflowport.RunListQuery) ([]workflowdomain.Run, int, error) {
	return nil, 0, nil
}

type contractControlRepo struct{}

func (contractControlRepo) GetRun(_ context.Context, _ string, _ string) (*workflowdomain.Run, error) {
	return nil, workflowdomain.ErrNotFound
}
func (contractControlRepo) ControlRun(_ context.Context, _ string, _ string, _ int64, _ workflowdomain.RunStatus, _ string, _ workflowdomain.Event) error {
	return nil
}
func (contractControlRepo) ListApprovals(_ context.Context, _ string, _ string, _ bool) ([]workflowdomain.Approval, error) {
	return nil, nil
}
func (contractControlRepo) DecideApproval(_ context.Context, _ string, _ string, _ int64, _ string, _ workflowdomain.ApprovalDecision, _ string, _ string, _ workflowdomain.Event) error {
	return nil
}
func (contractControlRepo) ListEffectIntents(_ context.Context, _ string, _ string) ([]workflowdomain.EffectIntent, error) {
	return nil, nil
}
func (contractControlRepo) ResolveEffect(_ context.Context, _ string, _ string, _ int64, _ workflowdomain.ManualAction, _ string, _ string, _ workflowdomain.Event) error {
	return nil
}

type contractAgentExecutor struct{}

func (contractAgentExecutor) ExecuteAgent(_ context.Context, _ string, _ string, _ string, _ string, _ string) (string, string, error) {
	return "", "", errors.New("stub: agent execution unavailable")
}

// ── IAM stubs ──────────────────────────────────────────────────────────────

type contractAdminUserRepo struct{}

func (contractAdminUserRepo) SearchUsers(_ context.Context, _ string, _ int) ([]iamport.AdminUser, error) {
	return nil, nil
}
func (contractAdminUserRepo) ListAdmins(_ context.Context) ([]iamport.AdminUser, error) {
	return nil, nil
}
func (contractAdminUserRepo) SetAdminRole(_ context.Context, _ string, _ string, _ *auditdomain.ResourceChangeAuditEvent) error {
	return nil
}
func (contractAdminUserRepo) RemoveAdminRole(_ context.Context, _ string, _ string, _ *auditdomain.ResourceChangeAuditEvent) error {
	return nil
}
func (contractAdminUserRepo) GetGlobalRole(_ context.Context, userID string) (iamdomain.GlobalRole, error) {
	if userID == "contract-user" {
		return iamdomain.GlobalRoleUser, nil
	}
	return iamdomain.GlobalRoleGlobalAdmin, nil
}

type contractAdminTenantRepo struct{}

func (contractAdminTenantRepo) Count(_ context.Context, _ iamdomain.TenantFilter) (int, error) {
	return 0, nil
}
func (contractAdminTenantRepo) List(_ context.Context, _ iamdomain.TenantFilter) ([]iamdomain.Tenant, error) {
	return nil, nil
}
func (contractAdminTenantRepo) Get(_ context.Context, _ string) (*iamdomain.Tenant, error) {
	// 返回有效租户：GetTenant 详情与 DeleteTenant 审计投影都依赖 Get 成功。
	return &iamdomain.Tenant{ID: "contract-id", Name: "contract-tenant", Slug: "contract-tenant", Plan: "free", Status: "active"}, nil
}
func (contractAdminTenantRepo) Create(_ context.Context, _ iamdomain.Tenant, _ string, _ *auditdomain.ResourceChangeAuditEvent) error {
	return nil
}
func (contractAdminTenantRepo) UpdatePatch(_ context.Context, _ string, _ iamdomain.TenantPatch, _ string, _ *auditdomain.ResourceChangeAuditEvent) error {
	return nil
}
func (contractAdminTenantRepo) HardDelete(_ context.Context, _ string, _ string, _ *auditdomain.ResourceChangeAuditEvent) error {
	return nil
}
func (contractAdminTenantRepo) ProvisionSchema(_ context.Context, _ string) error {
	return nil
}

type contractTenantRepo struct{}

func (contractTenantRepo) CountMembers(_ context.Context, _ string) (int, error) { return 0, nil }
func (contractTenantRepo) ListMembers(_ context.Context, _ string, _ int, _ int) ([]iamdomain.Member, error) {
	return nil, nil
}
func (contractTenantRepo) ListMembersByRole(_ context.Context, _ string, _ []string) ([]iamdomain.Member, error) {
	return nil, nil
}
func (contractTenantRepo) GetMemberRole(_ context.Context, _ string, _ string) (string, error) {
	return "member", nil
}
func (contractTenantRepo) UpdateMemberRole(_ context.Context, _ string, _ string, _ string) error {
	return nil
}
func (contractTenantRepo) DeleteMember(_ context.Context, _ string, _ string) error { return nil }
func (contractTenantRepo) GetTenantSettings(_ context.Context, _ string) (string, bool, []byte, error) {
	return "stub-tenant", false, []byte(`{}`), nil
}
func (contractTenantRepo) UpdateTenantName(_ context.Context, _ string, _ string) error { return nil }
func (contractTenantRepo) UpdateTenantSettings(_ context.Context, _ string, _ []byte) error {
	return nil
}
func (contractTenantRepo) ListUserTenants(_ context.Context, _ string) ([]iamdomain.UserTenantInfo, error) {
	return nil, nil
}

func (contractTenantRepo) ListAllTenants(context.Context) ([]iamdomain.UserTenantInfo, error) {
	return nil, nil
}

type contractInvitationRepo struct{}

func (contractInvitationRepo) Create(_ context.Context, _ iamdomain.TenantInvitation) error {
	return nil
}
func (contractInvitationRepo) ConsumeAndJoin(_ context.Context, _ iamdomain.InvitationJoinInput) (*iamdomain.InvitationJoinResult, error) {
	return nil, errStubNotFound
}
func (contractInvitationRepo) ConsumeAndJoinExisting(_ context.Context, _ iamdomain.ExistingInvitationJoinInput) (*iamdomain.InvitationJoinResult, error) {
	return nil, errStubNotFound
}

// ── Existing stubs ─────────────────────────────────────────────────────────

type contractDashboardRepo struct{}

func (contractDashboardRepo) Overview(context.Context, string) (platformdomain.DashboardOverview, error) {
	return platformdomain.DashboardOverview{}, nil
}

type contractAgentRepo struct{}

func (contractAgentRepo) Register(context.Context, *agentdomain.AgentConfig, *auditdomain.ResourceChangeAuditEvent, []string) error {
	return nil
}
func (contractAgentRepo) Get(context.Context, string) (*agentdomain.AgentConfig, bool, error) {
	return nil, false, nil
}
func (contractAgentRepo) GetAll(context.Context) ([]*agentdomain.AgentConfig, error) {
	return nil, nil
}
func (contractAgentRepo) Remove(context.Context, string, *auditdomain.ResourceChangeAuditEvent) error {
	return nil
}
func (contractAgentRepo) Update(context.Context, *agentdomain.AgentConfig, *auditdomain.ResourceChangeAuditEvent, string, bool, *versioningdomain.Version) error {
	return nil
}
func (contractAgentRepo) Rollback(context.Context, *agentdomain.AgentConfig, *auditdomain.ResourceChangeAuditEvent, string, string) error {
	return nil
}

// Operation gate stubs: self-modify always lands as a pending proposal, so
// the recorded response is the deterministic 202 pending_approval shape.
type contractOpPropRepo struct{}

func (contractOpPropRepo) Insert(context.Context, agentdomain.OperationProposal) error { return nil }
func (contractOpPropRepo) GetByID(context.Context, string, string) (*agentdomain.OperationProposal, error) {
	return nil, agentdomain.ErrOperationProposalNotFound
}
func (contractOpPropRepo) ListPending(context.Context, string) ([]agentdomain.OperationProposal, error) {
	return nil, nil
}
func (contractOpPropRepo) UpdateStatus(
	context.Context, string, string, agentdomain.OpProposalStatus, string, string,
) error {
	return nil
}
func (contractOpPropRepo) HasPending(context.Context, string, string) (bool, error) {
	return false, nil
}
func (contractOpPropRepo) ConsumeApproved(context.Context, string, string, string) (bool, error) {
	return false, nil
}
func (contractOpPropRepo) ListByProposer(context.Context, string, string) ([]agentdomain.OperationProposal, error) {
	return nil, nil
}
func (contractOpPropRepo) ListHistory(context.Context, string, string, int, int) ([]agentdomain.OperationProposal, int, error) {
	return nil, 0, nil
}

type contractOpUsageRepo struct{}

func (contractOpUsageRepo) AddUsage(
	context.Context, string, string, agentport.OperationType, time.Time, float64, int,
) error {
	return nil
}
func (contractOpUsageRepo) DailyUsage(
	context.Context, string, string, agentport.OperationType, time.Time,
) (agentport.DailyOperationUsage, error) {
	return agentport.DailyOperationUsage{}, nil
}

type contractTenantRole struct{}

func (contractTenantRole) ResolveTenantRole(context.Context, string, string) (string, error) {
	return "admin", nil
}

type contractProposalRepo struct{}

func (contractProposalRepo) Create(context.Context, agentdomain.ResourceChangeProposal, agentdomain.ProposalEvent) error {
	return nil
}
func (contractProposalRepo) Get(context.Context, string) (agentdomain.ResourceChangeProposal, error) {
	return agentdomain.ResourceChangeProposal{}, agentdomain.ErrProposalNotFound
}
func (contractProposalRepo) UpdateDraft(
	context.Context, agentdomain.ResourceChangeProposal, agentdomain.ProposalEvent,
) error {
	return nil
}
func (contractProposalRepo) Cancel(context.Context, string, string, time.Time) error  { return nil }
func (contractProposalRepo) Confirm(context.Context, string, string, time.Time) error { return nil }
func (contractProposalRepo) ClaimApplying(
	context.Context, string, string, time.Time,
) (agentdomain.ResourceChangeProposal, error) {
	return agentdomain.ResourceChangeProposal{}, agentdomain.ErrProposalNotFound
}
func (contractProposalRepo) Finish(
	context.Context, string, agentdomain.ProposalStatus, agentdomain.ApplyResult, agentdomain.ProposalEvent,
) error {
	return nil
}
func (contractProposalRepo) ListEvents(context.Context, string) ([]agentdomain.ProposalEvent, error) {
	return nil, agentdomain.ErrProposalNotFound
}

type contractProposalAuthorizer struct{}

func (contractProposalAuthorizer) AuthorizeProposal(
	context.Context, string, string, agentdomain.ResourceKind, agentdomain.ProposalOperation,
	agentdomain.ProposalAction,
) error {
	return nil
}

// ── Audit stub ─────────────────────────────────────────────────────────────

type contractAuditRepo struct{}

func (contractAuditRepo) List(_ context.Context, _ string, _ auditport.ResourceChangeAuditFilter) ([]auditport.ResourceChangeAuditRow, int, error) {
	return nil, 0, nil
}

func (contractAuditRepo) GetByID(_ context.Context, _, _ string) (*auditport.ResourceChangeAuditRow, error) {
	return nil, nil
}

// schedulerStub wires a real Service over stub ports so the DDD
// router records deterministic scheduled-task responses.
func schedulerStub(logger *zap.Logger) *schedapp.Service {
	return schedapp.NewService(contractSchedRepo{}, contractSchedRunner{}, contractSchedResolver{},
		observability.NoopMetrics{}, logger, func() string { return "contract-task" }, time.Now)
}

type contractSchedRepo struct{}

func (contractSchedRepo) Insert(context.Context, string, *scheddomain.ScheduledTask) error {
	return nil
}
func (contractSchedRepo) GetByID(context.Context, string, string) (*scheddomain.ScheduledTask, error) {
	return nil, scheddomain.ErrScheduledTaskNotFound
}
func (contractSchedRepo) List(context.Context, string, int, int) ([]scheddomain.ScheduledTask, int, error) {
	return nil, 0, nil
}
func (contractSchedRepo) Update(context.Context, string, *scheddomain.ScheduledTask) error {
	return nil
}
func (contractSchedRepo) Delete(context.Context, string, string) error { return nil }
func (contractSchedRepo) SetEnabled(context.Context, string, string, bool, *time.Time) error {
	return nil
}
func (contractSchedRepo) ListDue(context.Context, string, time.Time, int) ([]scheddomain.ScheduledTask, error) {
	return nil, nil
}
func (contractSchedRepo) RecordFire(context.Context, string, string, time.Time, string, string, time.Time, time.Time) (bool, error) {
	return true, nil
}

type contractSchedRunner struct{}

func (contractSchedRunner) StartAsync(context.Context, string, string, map[string]any, string, string) error {
	return nil
}

type contractSchedResolver struct{}

func (contractSchedResolver) GetVersion(context.Context, string, string) (*schedport.VersionInfo, error) {
	return &schedport.VersionInfo{DefinitionID: "contract-workflow"}, nil
}
func (contractSchedResolver) ValidateInput(context.Context, string, string, map[string]any) error {
	return nil
}
func (contractSchedResolver) ResolveVersionNames(context.Context, string, []string) (map[string]schedport.VersionName, error) {
	return nil, nil
}
