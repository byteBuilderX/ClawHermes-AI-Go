import { useCallback, useEffect, useRef, useState } from 'react';

import { evaluationApi } from '../api/evaluation.api';
import type { ResourceSummary } from '../model/evaluation';

import { extractErrorMessage } from '@/shared/lib';

// useRegisteredResources 提供当前可登记为评测目标的资源（agent/knowledge）列表：
// 「新建评测」目标下拉（CreateEvaluationModal 内部再按 stable_revision_id 过滤）与
// 「登记被测资源」已登记行提示共用同一份清单，避免各宿主各自重复拉取。
export const useRegisteredResources = () => {
  const [resources, setResources] = useState<ResourceSummary[]>([]);
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
    try {
      const page = await evaluationApi.listResources({ resource_kind: 'agent,knowledge' });
      if (mountedRef.current && generation === generationRef.current) setResources(page.items);
    } catch (err) {
      if (mountedRef.current && generation === generationRef.current) {
        setError(extractErrorMessage(err) || '加载被测资源失败');
      }
    } finally {
      if (mountedRef.current && generation === generationRef.current) setLoading(false);
    }
  }, []);

  useEffect(() => {
    mountedRef.current = true;
    return () => { mountedRef.current = false; generationRef.current += 1; };
  }, []);

  useEffect(() => { void load(); }, [load]);

  return { resources, loading, error, reload: () => load() };
};
