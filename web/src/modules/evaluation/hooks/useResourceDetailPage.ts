import { useCallback, useEffect, useRef, useState } from 'react';

import { evaluationApi } from '../api/evaluation.api';
import type {
  CandidateSummary,
  ExperimentSummary,
  ResourceKind,
  ResourceSummary,
  RunSummary,
  TimelineEvent,
} from '../model/evaluation';

import { extractErrorMessage } from '@/shared/lib';

interface ResourceDetailData {
  resource: ResourceSummary | null;
  runs: RunSummary[];
  candidates: CandidateSummary[];
  experiments: ExperimentSummary[];
  events: TimelineEvent[];
}

const EMPTY: ResourceDetailData = { resource: null, runs: [], candidates: [], experiments: [], events: [] };

interface Props { resourceKind?: ResourceKind; resourceId: string }

// useResourceDetailPage 以 eval 证据账本视角聚合单个被测资源：建档行 + 本资源全部
// runs/candidates/experiments + 版本时间线，五路并行取数。任一失败整页报错（证据
// 不全不假装可用）；resourceKind 缺失（非法 URL）时跳过取数保持空态，由页面兜底。
// resource 未建档时建档行列表为空 → resource=null，页面据此渲染登记引导。
export const useResourceDetailPage = ({ resourceKind, resourceId }: Props) => {
  const [data, setData] = useState<ResourceDetailData>(EMPTY);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const mountedRef = useRef(true);
  const generationRef = useRef(0);

  const load = useCallback(async () => {
    if (!mountedRef.current) return;
    const generation = generationRef.current + 1;
    generationRef.current = generation;
    setLoading(true);
    setError('');
    if (!resourceKind) {
      setData(EMPTY);
      setLoading(false);
      return;
    }
    try {
      const [resources, runs, candidates, experiments, timeline] = await Promise.all([
        evaluationApi.listResources({ resource_kind: resourceKind, resource_id: resourceId }),
        evaluationApi.listRuns({ resource_kind: resourceKind, resource_id: resourceId }),
        evaluationApi.listCandidates({ resource_kind: resourceKind, resource_id: resourceId }),
        evaluationApi.listExperiments({ resource_kind: resourceKind, resource_id: resourceId }),
        evaluationApi.getTimeline(resourceKind, resourceId),
      ]);
      if (!mountedRef.current || generation !== generationRef.current) return;
      const resource = resources.items.find((row) => row.resource_id === resourceId) ?? null;
      setData({
        resource,
        runs: runs.items,
        candidates: candidates.items,
        experiments: experiments.items,
        events: timeline.items,
      });
    } catch (err) {
      if (mountedRef.current && generation === generationRef.current) {
        setError(extractErrorMessage(err) || '加载资源证据失败');
      }
    } finally {
      if (mountedRef.current && generation === generationRef.current) setLoading(false);
    }
  }, [resourceKind, resourceId]);

  useEffect(() => {
    mountedRef.current = true;
    return () => { mountedRef.current = false; generationRef.current += 1; };
  }, []);

  useEffect(() => { void load(); }, [load]);

  return { ...data, loading, error, reload: () => load() };
};
