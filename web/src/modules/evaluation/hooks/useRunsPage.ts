import { useCallback, useEffect, useMemo, useRef, useState } from 'react';

import { evaluationApi } from '../api/evaluation.api';
import type { EvaluationCenterFilters, RunSummary } from '../model/evaluation';

import { extractErrorMessage } from '@/shared/lib';

// useRunsPage 承载「离线运行记录」页数据：按资源类型/资源/状态过滤离线评测 run。
// 与 useEvaluationCenter 相同的装载约定（generation guard + mountedRef），页面只拼装。
export const useRunsPage = (filters: EvaluationCenterFilters = {}) => {
  const stableFilters = useMemo(() => {
    const value: EvaluationCenterFilters = {};
    if (filters.resource_kind !== undefined) value.resource_kind = filters.resource_kind;
    if (filters.resource_id !== undefined) value.resource_id = filters.resource_id;
    if (filters.status !== undefined) value.status = filters.status;
    if (filters.cursor !== undefined) value.cursor = filters.cursor;
    if (filters.limit !== undefined) value.limit = filters.limit;
    return value;
  }, [filters.resource_kind, filters.resource_id, filters.status, filters.cursor, filters.limit]);
  const [runs, setRuns] = useState<RunSummary[]>([]);
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
      const page = await evaluationApi.listRuns(requestFilters);
      if (mountedRef.current && generation === requestGenerationRef.current) setRuns(page.items);
    } catch (err) {
      if (mountedRef.current && generation === requestGenerationRef.current) {
        setError(extractErrorMessage(err) || '加载运行记录失败');
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

  return { runs, loading, error, reload: () => load() };
};
