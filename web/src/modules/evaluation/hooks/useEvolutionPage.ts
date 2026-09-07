import { useCallback, useEffect, useMemo, useRef, useState } from 'react';

import { evaluationApi } from '../api/evaluation.api';
import type { CandidateSummary, EvaluationCenterFilters, ExperimentSummary } from '../model/evaluation';

import { extractErrorMessage } from '@/shared/lib';

// useEvolutionPage 承载「自进化工作区」数据：候选版本 + 金丝雀实验两条推进链路。
// 与被测收敛语义一致：默认聚合 agent/knowledge 两轨，可按 kind 单选过滤；演进类
// 命令（生成候选/建实验/晋级/回滚）由页面传入 RBAC 门禁后在命令流补齐。
export const useEvolutionPage = (filters: EvaluationCenterFilters = {}) => {
  const stableFilters = useMemo(() => {
    const value: EvaluationCenterFilters = {};
    if (filters.resource_kind !== undefined) value.resource_kind = filters.resource_kind;
    if (filters.resource_id !== undefined) value.resource_id = filters.resource_id;
    if (filters.status !== undefined) value.status = filters.status;
    return value;
  }, [filters.resource_kind, filters.resource_id, filters.status]);
  const [candidates, setCandidates] = useState<CandidateSummary[]>([]);
  const [experiments, setExperiments] = useState<ExperimentSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const filtersRef = useRef(stableFilters);
  const requestGenerationRef = useRef(0);
  const mountedRef = useRef(true);
  filtersRef.current = stableFilters;

  const load = useCallback(async () => {
    if (!mountedRef.current) return;
    const generation = requestGenerationRef.current + 1;
    requestGenerationRef.current = generation;
    const requestFilters = filtersRef.current;
    setLoading(true);
    setError('');
    try {
      const [candidatePage, experimentPage] = await Promise.all([
        evaluationApi.listCandidates(requestFilters),
        evaluationApi.listExperiments(requestFilters),
      ]);
      if (mountedRef.current && generation === requestGenerationRef.current) {
        setCandidates(candidatePage.items);
        setExperiments(experimentPage.items);
      }
    } catch (err) {
      if (mountedRef.current && generation === requestGenerationRef.current) {
        setError(extractErrorMessage(err) || '加载自进化工作区失败');
      }
    } finally {
      if (mountedRef.current && generation === requestGenerationRef.current) setLoading(false);
    }
  }, []);

  useEffect(() => {
    mountedRef.current = true;
    return () => { mountedRef.current = false; requestGenerationRef.current += 1; };
  }, []);

  useEffect(() => { void load(); }, [load, stableFilters]);

  return { candidates, experiments, loading, error, reload: () => load() };
};
