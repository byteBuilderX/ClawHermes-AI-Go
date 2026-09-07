import { useCallback, useRef } from 'react';

import { evaluationApi } from '../api/evaluation.api';
import type { CreateEvaluationPlan, EvaluationJob } from '../model/evaluation';

import { useTenantRole } from '@/modules/iam';
import { createIdempotencyKey } from '@/shared/lib/idempotencyKey';

const MANAGEMENT_ERROR = '仅租户管理员可执行评测命令';

interface CreateWorkflow {
  fingerprint: string;
  suiteID?: string;
  publishedRevisionID?: string;
  idempotencyKey: string;
  inFlight?: Promise<EvaluationJob>;
}

// useCreateEvaluation 承载「新建一次评测 run」的三态执行（S3 产品决策），自
// useEvaluationCenter 抽取供离线运行页复用：
//  - published：目标评测集已有 published revision，纯读直接 enqueueRun，不写套件。
//  - unpublished：套件已存在但从未发布，先把 draft publish 成 v1，再 enqueueRun。
//  - create：内联建含起始 case 的新套件 → publish v1 → enqueueRun。
// 指纹 + idempotency_key + in-flight 去重 + workflow ref 内 suiteID/publishedRevisionID
// 的缓存语义决定重试：同一 fingerprint 下部分成功（已建套件/已发布）后重试不会二次
// createSuite / publishSuite，且失败重试沿用同一 idempotency_key。
export const useCreateEvaluation = () => {
  const { isAdmin } = useTenantRole();
  const canManageEvaluation = isAdmin;
  const workflowRef = useRef<CreateWorkflow>();

  const resetCreateEvaluation = useCallback(() => { workflowRef.current = undefined; }, []);

  const createEvaluation = useCallback((plan: CreateEvaluationPlan): Promise<EvaluationJob> => {
    if (!canManageEvaluation) return Promise.reject(new Error(MANAGEMENT_ERROR));
    const fingerprint = JSON.stringify(plan);
    let workflow = workflowRef.current;
    if (!workflow || workflow.fingerprint !== fingerprint) {
      workflow = { fingerprint, idempotencyKey: createIdempotencyKey() };
      workflowRef.current = workflow;
    }
    if (workflow.inFlight) return workflow.inFlight;
    const current = workflow;
    current.inFlight = (async () => {
      let revisionId: string;
      if (plan.mode === 'published') {
        // 已发布评测集：直接以计划携带的 published revision 排队，无任何写操作。
        revisionId = plan.revisionId;
      } else if (plan.mode === 'unpublished') {
        // 未发布草稿：发布成 v1 后排队；发布成功但入队失败的重试复用已发布 revision。
        if (!current.publishedRevisionID) {
          const published = await evaluationApi.publishSuite(plan.suiteId);
          current.publishedRevisionID = published.id;
        }
        revisionId = current.publishedRevisionID;
      } else {
        // create：内联建套件（含起始 case）→ 发布 → 排队；重试复用已建套件与已发布 revision。
        if (!current.suiteID) {
          const created = await evaluationApi.createSuite({ name: plan.name, description: plan.description,
            resourceKind: plan.resource.kind, cases: plan.cases });
          current.suiteID = created.suite.id;
        }
        if (!current.publishedRevisionID) {
          const published = await evaluationApi.publishSuite(current.suiteID);
          current.publishedRevisionID = published.id;
        }
        revisionId = current.publishedRevisionID;
      }
      const job = await evaluationApi.enqueueRun(plan.resource, revisionId, current.idempotencyKey);
      if (workflowRef.current === current) resetCreateEvaluation();
      return job;
    })().finally(() => { current.inFlight = undefined; });
    return current.inFlight;
  }, [canManageEvaluation, resetCreateEvaluation]);

  return { createEvaluation, resetCreateEvaluation, canManageEvaluation };
};
