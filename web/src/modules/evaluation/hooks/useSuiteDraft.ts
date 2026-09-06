import { useCallback, useEffect, useRef, useState } from 'react';

import { evaluationApi } from '../api/evaluation.api';
import type { AddDraftCaseValues } from '../components/AddDraftCaseModal';
import type { EditDraftCaseValues } from '../components/EditDraftCaseModal';
import type { GenerateCasesValues } from '../components/GenerateCasesModal';
import type { SuiteRevision } from '../model/evaluation';

import { extractErrorMessage } from '@/shared/lib';

const DRAFT_MANAGEMENT_ERROR = '仅租户管理员可编辑评测集草稿';

// useSuiteDraft 承载评测集「草稿编辑」的全部数据与动作，只服务已存在草稿的套件
// （由详情页按 detail.draft_revision_id 是否为空决定是否挂载本 hook；legacy 补建
// 草稿走单独方法 startNextDraft 后同样落入本 hook 管辖）。写操作受 enabled
// （调用方传入 isAdmin）门禁 fail-closed；错误一律向上抛出，由页面统一 message。
// 每个写动作成功后内部先 reload 草稿再返回结果，调用方按需刷新 detail 元信息。
export const useSuiteDraft = ({ suiteId, enabled }: { suiteId: string; enabled: boolean }) => {
  const [draft, setDraft] = useState<SuiteRevision | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const mountedRef = useRef(true);
  const generationRef = useRef(0);

  const reload = useCallback(async () => {
    if (!mountedRef.current) return;
    const generation = generationRef.current + 1;
    generationRef.current = generation;
    setLoading(true); setError('');
    try {
      const next = await evaluationApi.getSuiteDraft(suiteId);
      if (mountedRef.current && generation === generationRef.current) setDraft(next);
    } catch (err) {
      if (mountedRef.current && generation === generationRef.current) {
        setError(extractErrorMessage(err) || '加载草稿用例失败');
      }
    } finally {
      if (mountedRef.current && generation === generationRef.current) setLoading(false);
    }
  }, [suiteId]);

  useEffect(() => {
    mountedRef.current = true;
    return () => { mountedRef.current = false; generationRef.current += 1; };
  }, []);

  useEffect(() => { void reload(); }, [reload]);

  const manage = useCallback(async <T,>(operation: () => Promise<T>): Promise<T> => {
    if (!enabled) throw new Error(DRAFT_MANAGEMENT_ERROR);
    const result = await operation();
    await reload();
    return result;
  }, [enabled, reload]);

  return {
    draft, loading, error, reload,
    publish: () => manage(() => evaluationApi.publishSuite(suiteId)),
    generate: (values: GenerateCasesValues) => manage(() => evaluationApi.generateSuiteCases(suiteId, values)),
    saveCase: (caseId: string, values: EditDraftCaseValues) =>
      manage(() => evaluationApi.updateDraftCase(suiteId, caseId, values)),
    deleteCase: (caseId: string) => manage(async () => { await evaluationApi.deleteDraftCase(suiteId, caseId); }),
    addCase: (values: AddDraftCaseValues) => manage(() => evaluationApi.addDraftCase(suiteId, values)),
    // startNextDraft 补建草稿（legacy：从当前 active revision 继承 cases）后 reload
    // 草稿；页面随后刷新 detail 以切换回草稿编辑视图。
    startNextDraft: () => manage(() => evaluationApi.startNextDraft(suiteId)),
  };
};
