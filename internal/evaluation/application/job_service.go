package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
	"github.com/google/uuid"
)

type StoredRunner interface {
	RunStored(
		ctx context.Context, tenantID, requestedBy string, resource domain.ResourceRef, suiteRevisionID string,
		snapshot *domain.EvaluationContextSnapshot,
	) (domain.EvalRun, error)
}

var ErrJobNotFound = errors.New("evaluation job not found")

type EnqueueRunInput struct {
	Resource             domain.ResourceRef
	SuiteRevisionID      string
	IdempotencyKey       string
	RequestedBy          string
	PlatformSeqOverrides map[string]int64
}

type JobService struct {
	repo     port.JobRepository
	runner   StoredRunner
	capturer port.SnapshotCapturer
}

func NewJobService(repo port.JobRepository, runner StoredRunner, capturer port.SnapshotCapturer) *JobService {
	return &JobService{repo: repo, runner: runner, capturer: capturer}
}

func (s *JobService) EnqueueRun(ctx context.Context, tenantID string, input EnqueueRunInput) (domain.EvaluationJob, error) {
	if err := input.Resource.Validate(); err != nil {
		return domain.EvaluationJob{}, err
	}
	if input.SuiteRevisionID == "" {
		return domain.EvaluationJob{}, errors.New("suite revision id required")
	}
	if input.IdempotencyKey == "" {
		return domain.EvaluationJob{}, errors.New("idempotency key required")
	}
	if input.RequestedBy == "" {
		return domain.EvaluationJob{}, errors.New("requesting user id required")
	}
	if s.capturer == nil {
		return domain.EvaluationJob{}, errors.New("evaluation job: snapshot capturer not configured")
	}
	snapshot, err := s.capturer.Capture(ctx, tenantID, port.CaptureInput{
		Resource: input.Resource, SuiteRevisionID: input.SuiteRevisionID, RequestedBy: input.RequestedBy,
		PlatformSeqOverrides: input.PlatformSeqOverrides,
	})
	if err != nil {
		return domain.EvaluationJob{}, fmt.Errorf("evaluation job: capture context snapshot: %w", err)
	}
	// 纯防御：capturer 返回 (nil, nil) 会入队一个执行时必失败的任务，创建时即拒绝。
	if snapshot == nil {
		return domain.EvaluationJob{}, errors.New("evaluation job: capture context snapshot returned nil")
	}
	job := domain.EvaluationJob{
		ID: uuid.Must(uuid.NewV7()).String(), Type: domain.JobTypeEvalRun, Status: domain.JobQueued,
		Payload: domain.EvalRunJobPayload{
			Resource: input.Resource, SuiteRevisionID: input.SuiteRevisionID, RequestedBy: input.RequestedBy,
			Snapshot: snapshot,
		},
		IdempotencyKey: input.IdempotencyKey, CreatedBy: input.RequestedBy, CreatedAt: time.Now().UTC(),
	}
	return s.repo.Enqueue(ctx, tenantID, job)
}

func (s *JobService) Get(ctx context.Context, tenantID, jobID string) (domain.EvaluationJob, error) {
	job, ok, err := s.repo.Get(ctx, tenantID, jobID)
	if err != nil {
		return domain.EvaluationJob{}, err
	}
	if !ok {
		return domain.EvaluationJob{}, ErrJobNotFound
	}
	return job, nil
}

func (s *JobService) RunOnce(
	ctx context.Context,
	tenantID, workerID string,
	lease time.Duration,
) (bool, error) {
	job, err := s.repo.Claim(ctx, tenantID, workerID, lease)
	if err != nil || job == nil {
		return false, err
	}
	if job.Type != domain.JobTypeEvalRun {
		err := fmt.Errorf("unsupported evaluation job type: %s", job.Type)
		_ = s.repo.Fail(ctx, tenantID, job.ID, err.Error())
		return true, err
	}
	if s.runner == nil {
		err := errors.New("stored evaluation runner not configured")
		_ = s.repo.Fail(ctx, tenantID, job.ID, err.Error())
		return true, err
	}
	if strings.TrimSpace(job.Payload.RequestedBy) == "" {
		err := errors.New("evaluation job requesting user identity missing; enqueue the run again")
		return true, errors.Join(err, s.repo.Fail(ctx, tenantID, job.ID, err.Error()))
	}
	run, err := s.runner.RunStored(
		ctx, tenantID, job.Payload.RequestedBy, job.Payload.Resource, job.Payload.SuiteRevisionID, job.Payload.Snapshot,
	)
	if err != nil {
		_ = s.repo.Fail(ctx, tenantID, job.ID, err.Error())
		return true, err
	}
	if err := s.repo.Complete(ctx, tenantID, job.ID, run.ID); err != nil {
		return true, err
	}
	return true, nil
}
