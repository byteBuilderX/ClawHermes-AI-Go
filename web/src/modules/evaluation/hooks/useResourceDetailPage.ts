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

// useResourceDetailPage 以 eval 证据账本视角聚合单个被测资源。两段式取数：先定位建档行
// （listResources）。未登记资源建档行列表为空 → 空态短路并跳过其余取数，页面据此渲染登记
// 引导——后端对未登记资源的 Timeline 返回 404，若并入并行批会把「登记引导」误打成整页
// 错误。已建档才并行取本资源全部 runs/candidates/experiments + 版本时间线，任一失败整页
// 报错（证据不全不假装可用）。resourceKind 缺失（非法 URL）时跳过取数保持空态。
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
      // 第一段：定位建档行。listResources 对未登记资源返回空 200（存在性由建档行列表
      // 决定）；未登记 → 空态短路，跳过其余取数，避免后端 Timeline 对不存在资源的 404
      // 被并行批吞成整页错误而盖掉登记引导。已建档才进第二段并行取证据。
      const resources = await evaluationApi.listResources({ resource_kind: resourceKind, resource_id: resourceId });
      if (!mountedRef.current || generation !== generationRef.current) return;
      const resource = resources.items.find((row) => row.resource_id === resourceId) ?? null;
      if (!resource) {
        setData(EMPTY);
        return;
      }
      const [runs, candidates, experiments, timeline] = await Promise.all([
        evaluationApi.listRuns({ resource_kind: resourceKind, resource_id: resourceId }),
        evaluationApi.listCandidates({ resource_kind: resourceKind, resource_id: resourceId }),
        evaluationApi.listExperiments({ resource_kind: resourceKind, resource_id: resourceId }),
        evaluationApi.getTimeline(resourceKind, resourceId),
      ]);
      if (!mountedRef.current || generation !== generationRef.current) return;
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
