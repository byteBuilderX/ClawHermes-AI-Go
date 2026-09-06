package contracttest

import (
	"crypto/rsa"
	"fmt"
	"sync/atomic"

	"go.uber.org/zap"

	"github.com/byteBuilderX/stratum/api/wiring"
	"github.com/byteBuilderX/stratum/config"
	agentapp "github.com/byteBuilderX/stratum/internal/agent/application"
	evalapp "github.com/byteBuilderX/stratum/internal/evaluation/application"
	iamapp "github.com/byteBuilderX/stratum/internal/iam/application"
	iamtoken "github.com/byteBuilderX/stratum/internal/iam/infrastructure/token"
	llmapp "github.com/byteBuilderX/stratum/internal/llmgateway/application"
	platformapp "github.com/byteBuilderX/stratum/internal/platform/application"
	workflowapp "github.com/byteBuilderX/stratum/internal/workflow/application"
	"github.com/byteBuilderX/stratum/pkg/observability"
)

// BuildContainer 装配契约 harness 用的确定性 DDD 容器：全部 stub 注入
// 各域 service，admin/tenant 角色固定（contractTenantRole 恒返 "admin"）。
// 这是 contract_test.go（校验 golden）与 scripts/record-contracts.go
// （录制 golden）共用的唯一事实源，字段装配语义与 TestContracts 内的
// 容器字面量逐一对应（超集，含 ObservationService）。
func BuildContainer(cfg *config.Config, key *rsa.PrivateKey, logger *zap.Logger, metrics *observability.PrometheusMetrics) *wiring.Container {
	var idCounter atomic.Int64
	nextID := func() string { return fmt.Sprintf("contract-%d", idCounter.Add(1)) }
	return &wiring.Container{
		Config: cfg, Logger: logger,
		Platform: &wiring.Platform{
			JWTService: iamtoken.NewJWTService(key), Metrics: metrics,
			DashboardService: platformapp.NewDashboardService(contractDashboardRepo{}),
		},
		LLMGateway: &wiring.LLMGateway{
			ProviderService:  llmapp.NewProviderService(contractProviderRepo{}, contractModelRepo{}, contractProviderRuntime{}),
			ModelMgmtService: llmapp.NewModelMgmtService(contractModelRepo{}),
		},
		Skill: &wiring.Skill{}, MCP: &wiring.MCP{}, Memory: &wiring.Memory{},
		Agent: func() *wiring.Agent {
			gate := agentapp.NewOperationGateService(
				contractOpPropRepo{}, contractOpUsageRepo{}, metrics,
			)
			svc := agentapp.NewAgentService(agentapp.AgentServiceDeps{
				Registry: agentapp.NewRegistry(contractAgentRepo{}, logger),
				Logger:   logger,
				Metrics:  metrics,
			})
			svc.SetOperationGate(gate)
			return &wiring.Agent{
				ProposalService: agentapp.NewResourceChangeProposalService(
					contractProposalRepo{}, contractProposalAuthorizer{}, nil, nil, metrics,
				),
				OperationGateService: gate,
				OperationProposalSvc: agentapp.NewOperationProposalService(
					contractOpPropRepo{}, contractTenantRole{}, metrics,
				),
				Service: svc,
			}
		}(),
		Workflow: &wiring.Workflow{
			DefinitionService: func() *workflowapp.DefinitionService {
				svc := workflowapp.NewDefinitionService(contractDefRepo{}, contractVersionRepo{}, nextID)
				// 所有权矩阵单事实源：契约 harness 固定 admin 角色，注入后
				// admin 的 Update/Publish/Validate 走 OpEdit 放行，Delete 走
				// createdBy==actorID 校验（stub 空 createdBy → 403，预期语义）。
				svc.SetTenantRoleResolver(contractTenantRole{})
				return svc
			}(),
			RunService:     workflowapp.NewRunService(contractVersionRepo{}, contractRunStore{}, contractAgentExecutor{}, nextID),
			ControlService: workflowapp.NewControlService(contractControlRepo{}, nextID),
		},
		Knowledge: &wiring.Knowledge{},
		Evaluation: &wiring.Evaluation{
			SuiteService: evalapp.NewSuiteService(nil), JobService: evalapp.NewJobService(nil, nil, nil),
			QueryService:      evalapp.NewQueryService(contractQueryRepo{}),
			ExperimentService: evalapp.NewExperimentService(contractExperimentRepo{}),
			CandidateService:  evalapp.NewCandidateCommandService(contractCandidateRepo{}),
			ObservationService: evalapp.NewObservationService(evalapp.ObservationServiceDeps{
				Repo: contractObservationRepo{}, Logger: logger,
			}),
			ReviewService: evalapp.NewReviewService(evalapp.ReviewServiceDeps{
				Repo: contractReviewRepo{}, Logger: logger,
			}),
		},
		IAM: &wiring.IAM{
			AdminService: iamapp.NewAdminService(
				contractAdminTenantRepo{},
				iamapp.WithUserRepo(contractAdminUserRepo{}),
			),
			TenantService:     iamapp.NewTenantService(contractTenantRepo{}, logger),
			InvitationService: iamapp.NewInvitationService(contractInvitationRepo{}),
		},
		Scheduler: &wiring.Scheduler{Service: schedulerStub(logger)},
		Audit:     &wiring.Audit{QueryService: contractAuditRepo{}},
	}
}
