import { useCallback, useEffect, useRef, useState } from 'react';

import { evaluationApi } from '../api/evaluation.api';
import type { RunSummary } from '../model/evaluation';

import { extractErrorMessage } from '@/shared/lib';

interface RunDetailData { run: RunSummary | null; runs: RunSummary[] }

const EMPTY: RunDetailData = { run: null, runs: [] };

interface Props { runId: string }

// useRunDetailPage 承载单条离线运行详情页：先 getRun 校验 id 存在并取得该运行的
// 资源身份（kind+resource_id），再按该资源 listRuns 取回含当前运行在内的回归行，
// 供 RunDetailView 的版本对比复用。getRun 内部再次拉详情是展示体自取锚点/指标/
// 归因的设计（RunDetailView 由运行详情页挂载），此处仅取资源身份与摘要行。
// 超窗（不在该资源最近列表页内）的深链回退为未建档空态，由页面兜底。
export const useRunDetailPage = ({ runId }: Props) => {
  const [data, setData] = useState<RunDetailData>(EMPTY);
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
    if (!runId) {
      setData(EMPTY);
      setLoading(false);
      return;
    }
    try {
      const detail = await evaluationApi.getRun(runId);
      const page = await evaluationApi.listRuns({
        resource_kind: detail.resource.kind,
        resource_id: detail.resource.resource_id,
      });
      if (!mountedRef.current || generation !== generationRef.current) return;
      const run = page.items.find((item) => item.id === runId) ?? null;
      setData({ run, runs: page.items });
    } catch (err) {
      if (mountedRef.current && generation === generationRef.current) {
        setError(extractErrorMessage(err) || '加载运行详情失败');
      }
    } finally {
      if (mountedRef.current && generation === generationRef.current) setLoading(false);
    }
  }, [runId]);

  useEffect(() => {
    mountedRef.current = true;
    return () => { mountedRef.current = false; generationRef.current += 1; };
  }, []);

  useEffect(() => { void load(); }, [load]);

  return { ...data, loading, error, reload: () => load() };
};
