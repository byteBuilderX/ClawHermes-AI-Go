import { useCallback, useEffect, useMemo, useRef, useState } from 'react';

import { evaluationApi } from '../api/evaluation.api';
import type { EvaluationCenterFilters, ResourceSummary } from '../model/evaluation';

import { extractErrorMessage } from '@/shared/lib';

// useResourceListPage 承载「被测资源」列表页数据：按 kind 过滤建档资源行。
// skill/mcp 历史资源只读保留，agent/knowledge 当前被测；列表复用 ResourceTable。
export const useResourceListPage = (filters: EvaluationCenterFilters = {}) => {
  const stableFilters = useMemo(() => {
    const value: EvaluationCenterFilters = {};
    if (filters.resource_kind !== undefined) value.resource_kind = filters.resource_kind;
    if (filters.status !== undefined) value.status = filters.status;
    return value;
  }, [filters.resource_kind, filters.status]);
  const [resources, setResources] = useState<ResourceSummary[]>([]);
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
      const page = await evaluationApi.listResources(requestFilters);
      if (mountedRef.current && generation === requestGenerationRef.current) setResources(page.items);
    } catch (err) {
      if (mountedRef.current && generation === requestGenerationRef.current) {
        setError(extractErrorMessage(err) || '加载被测资源失败');
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

  return { resources, loading, error, reload: () => load() };
};
