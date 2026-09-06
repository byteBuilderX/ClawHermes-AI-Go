package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	auditdomain "github.com/byteBuilderX/stratum/internal/audit/domain"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/pkg/storage/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PgJobRepository struct {
	pool poolIface
}

func NewPgJobRepository(pool *pgxpool.Pool) *PgJobRepository {
	return &PgJobRepository{pool: pool}
}

func (r *PgJobRepository) Enqueue(
	ctx context.Context,
	tenantID string,
	job domain.EvaluationJob,
) (domain.EvaluationJob, error) {
	payload, err := json.Marshal(job.Payload)
	if err != nil {
		return domain.EvaluationJob{}, fmt.Errorf("evaluation job repository: marshal payload: %w", err)
	}
	var saved domain.EvaluationJob
	err = r.execTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var payloadJSON []byte
		var status string
		if err := tx.QueryRow(ctx,
			// 幂等冲突按状态分派：succeeded/running 是静默 no-op（已完成或
			// 正在执行的 run 不重复触发）；failed/cancelled 重新激活为 queued
			// 并清空执行残留（error/lease/result），使重触发可被 worker 重拾。
			// 修复前 ON CONFLICT 仅写回幂等键，而 Claim 只选 queued/running，
			// 终态 job 永不重拾——矩阵重评测对失败 run 是静默 no-op。
			`INSERT INTO evaluation_jobs (id, job_type, payload, status, idempotency_key, created_by, created_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7)
			 ON CONFLICT (idempotency_key) DO UPDATE SET
			   status = CASE WHEN evaluation_jobs.status IN ('failed','cancelled') THEN 'queued' ELSE evaluation_jobs.status END,
			   error_message = CASE WHEN evaluation_jobs.status IN ('failed','cancelled') THEN '' ELSE evaluation_jobs.error_message END,
			   lease_owner = CASE WHEN evaluation_jobs.status IN ('failed','cancelled') THEN '' ELSE evaluation_jobs.lease_owner END,
			   lease_until = CASE WHEN evaluation_jobs.status IN ('failed','cancelled') THEN NULL ELSE evaluation_jobs.lease_until END,
			   result_id = CASE WHEN evaluation_jobs.status IN ('failed','cancelled') THEN '' ELSE evaluation_jobs.result_id END,
			   updated_at = CASE WHEN evaluation_jobs.status IN ('failed','cancelled') THEN NOW() ELSE evaluation_jobs.updated_at END
			 RETURNING id, job_type, payload, status, attempts, idempotency_key, error_message, result_id, created_by, created_at`,
			job.ID, job.Type, string(payload), string(job.Status), job.IdempotencyKey, job.CreatedBy, job.CreatedAt,
		).Scan(&saved.ID, &saved.Type, &payloadJSON, &status, &saved.Attempts,
			&saved.IdempotencyKey, &saved.ErrorMessage, &saved.ResultID, &saved.CreatedBy, &saved.CreatedAt); err != nil {
			return err
		}
		if err := json.Unmarshal(payloadJSON, &saved.Payload); err != nil {
			return fmt.Errorf("evaluation job repository: unmarshal payload: %w", err)
		}
		saved.Status = domain.JobStatus(status)
		return nil
	})
	return saved, err
}

func (r *PgJobRepository) Get(
	ctx context.Context,
	tenantID, jobID string,
) (domain.EvaluationJob, bool, error) {
	var job domain.EvaluationJob
	found := false
	err := r.execTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var payloadJSON []byte
		var status string
		err := tx.QueryRow(ctx,
			`SELECT id, job_type, payload, status, attempts, idempotency_key, error_message, result_id, created_at
			 FROM evaluation_jobs WHERE id=$1`, jobID,
		).Scan(&job.ID, &job.Type, &payloadJSON, &status, &job.Attempts,
			&job.IdempotencyKey, &job.ErrorMessage, &job.ResultID, &job.CreatedAt)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		job.Status = domain.JobStatus(status)
		return json.Unmarshal(payloadJSON, &job.Payload)
	})
	return job, found, err
}

func (r *PgJobRepository) Claim(
	ctx context.Context,
	tenantID, workerID string,
	lease time.Duration,
) (*domain.EvaluationJob, error) {
	var claimed *domain.EvaluationJob
	err := r.execTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var job domain.EvaluationJob
		var payloadJSON []byte
		var status string
		err := tx.QueryRow(ctx,
			`SELECT id, job_type, payload, status, attempts, idempotency_key, error_message, result_id, created_at
			 FROM evaluation_jobs
			 WHERE status='queued' OR (status='running' AND lease_until < NOW())
			 ORDER BY created_at
			 FOR UPDATE SKIP LOCKED LIMIT 1`,
		).Scan(&job.ID, &job.Type, &payloadJSON, &status, &job.Attempts,
			&job.IdempotencyKey, &job.ErrorMessage, &job.ResultID, &job.CreatedAt)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE evaluation_jobs
			 SET status='running', attempts=attempts+1, lease_owner=$2,
			     lease_until=NOW()+make_interval(secs => $3), updated_at=NOW()
			 WHERE id=$1`, job.ID, workerID, lease.Seconds()); err != nil {
			return err
		}
		if err := json.Unmarshal(payloadJSON, &job.Payload); err != nil {
			return err
		}
		job.Status = domain.JobRunning
		job.Attempts++
		claimed = &job
		return nil
	})
	return claimed, err
}

// EnqueuePlatformVerify 幂等插入平台验证任务（job_type=platform_verify，payload 按
// pgx v5 JSONB 规则先 json.Marshal 再以 string 传）。ON CONFLICT (idempotency_key)
// DO NOTHING：已存在（含终态）静默不覆盖；返回是否新插入（冲突 → false，调用方据此
// 不重复 +queued）。
func (r *PgJobRepository) EnqueuePlatformVerify(
	ctx context.Context,
	tenantID string,
	p domain.PlatformVerifyPayload,
	idempotencyKey, createdBy string,
) (bool, error) {
	payload, err := json.Marshal(p)
	if err != nil {
		return false, fmt.Errorf("evaluation job repository: marshal platform verify payload: %w", err)
	}
	inserted := false
	err = r.execTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`INSERT INTO evaluation_jobs (id, job_type, payload, status, idempotency_key, created_by, created_at)
			 VALUES ($1,$2,$3,'queued',$4,$5,NOW())
			 ON CONFLICT (idempotency_key) DO NOTHING`,
			uuid.Must(uuid.NewV7()).String(), domain.JobTypePlatformVerify, string(payload), idempotencyKey, createdBy)
		if err != nil {
			return err
		}
		inserted = tag.RowsAffected() > 0
		return nil
	})
	return inserted, err
}

// ClaimPlatformVerify 只取本租户一条 platform_verify 任务（queued 或 running 且 lease
// 过期）并置 running 续租。任务完成判定/幂等由调用方处理（多租户验证无终态写入——
// 回滚事件级任务由 idempotency_key 约束，P2 无 enqueue 调用方，见开放问题 #2）。
func (r *PgJobRepository) ClaimPlatformVerify(
	ctx context.Context,
	tenantID, workerID string,
	lease time.Duration,
) (*domain.PlatformVerifyJob, error) {
	var claimed *domain.PlatformVerifyJob
	err := r.execTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var job domain.PlatformVerifyJob
		var payloadJSON []byte
		err := tx.QueryRow(ctx,
			`SELECT id, payload
			 FROM evaluation_jobs
			 WHERE job_type=$1 AND (status='queued' OR (status='running' AND lease_until < NOW()))
			 ORDER BY created_at
			 FOR UPDATE SKIP LOCKED LIMIT 1`,
			domain.JobTypePlatformVerify,
		).Scan(&job.ID, &payloadJSON)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE evaluation_jobs
			 SET status='running', attempts=attempts+1, lease_owner=$2,
			     lease_until=NOW()+make_interval(secs => $3), updated_at=NOW()
			 WHERE id=$1`, job.ID, workerID, lease.Seconds()); err != nil {
			return err
		}
		if err := json.Unmarshal(payloadJSON, &job.Payload); err != nil {
			return fmt.Errorf("evaluation job repository: unmarshal platform verify payload: %w", err)
		}
		job.TenantID = tenantID
		claimed = &job
		return nil
	})
	return claimed, err
}

func (r *PgJobRepository) Complete(ctx context.Context, tenantID, jobID, resultID string) error {
	return r.execTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE evaluation_jobs
			 SET status=$2, result_id=$3, error_message='', lease_owner='', lease_until=NULL, updated_at=NOW()
			 WHERE id=$1`, jobID, string(domain.JobSucceeded), resultID)
		return err
	})
}

func (r *PgJobRepository) Fail(ctx context.Context, tenantID, jobID, errorMessage string) error {
	return r.setTerminal(ctx, tenantID, jobID, domain.JobFailed, errorMessage)
}

func (r *PgJobRepository) setTerminal(
	ctx context.Context,
	tenantID, jobID string,
	status domain.JobStatus,
	errorMessage string,
) error {
	return r.execTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE evaluation_jobs
			 SET status=$2, error_message=$3, lease_owner='', lease_until=NULL, updated_at=NOW()
			 WHERE id=$1`, jobID, string(status), errorMessage)
		return err
	})
}

func (r *PgJobRepository) execTenant(
	ctx context.Context,
	tenantID string,
	fn func(context.Context, pgx.Tx) error,
) error {
	ctx = postgres.WithTenant(ctx, &postgres.TenantContext{TenantID: tenantID})
	return execTenantTx(ctx, r.pool, tenantID, fn)
}

// GetJobCreatedBy 返回执行任务创建者；未命中 found=false。
func (r *PgJobRepository) GetJobCreatedBy(ctx context.Context, tenantID, jobID string) (string, bool, error) {
	var createdBy string
	found := false
	err := r.execTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT created_by FROM evaluation_jobs WHERE id=$1`, jobID).Scan(&createdBy)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return fmt.Errorf("evaluation job repository: load created by: %w", err)
		}
		found = true
		return nil
	})
	return createdBy, found, err
}

// DeleteJob 删除执行任务：running 任务拒删（FOR UPDATE 锁定避免与 worker lease
// 竞争），queued 与终态可删；删除同事务写审计。
func (r *PgJobRepository) DeleteJob(
	ctx context.Context, tenantID, jobID string, audit *auditdomain.ResourceChangeAuditEvent,
) error {
	return r.execTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var status string
		err := tx.QueryRow(ctx,
			`SELECT status FROM evaluation_jobs WHERE id=$1 FOR UPDATE`, jobID).Scan(&status)
		if err == pgx.ErrNoRows {
			return fmt.Errorf("evaluation job repository: delete job %s: not found", jobID)
		}
		if err != nil {
			return fmt.Errorf("evaluation job repository: load job status: %w", err)
		}
		if status == string(domain.JobRunning) {
			return domain.ErrEntityReferenced
		}
		tag, err := tx.Exec(ctx, `DELETE FROM evaluation_jobs WHERE id=$1`, jobID)
		if err != nil {
			return translateEntityReferenced(fmt.Errorf("evaluation job repository: delete job: %w", err))
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("evaluation job repository: delete job %s: not found", jobID)
		}
		return insertChangeAudit(ctx, tx, audit)
	})
}
