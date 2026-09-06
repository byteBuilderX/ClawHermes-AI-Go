import { Alert, Button, Empty, Select, Space, Spin } from 'antd';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';

import { evaluationApi } from '../api/evaluation.api';
import type { ResourceKind, SuiteRevisionMeta, SuiteSummary } from '../model/evaluation';

/** SuitePick 描述一次「评测集 → published 版本」选择。
 *  - 已发布套件：revisionId 指向所选 published revision（默认当前 active 版本）。
 *  - 尚未发布的草稿套件（allowUnpublished）：只返回 suiteId，由调用方先发布 v1 再运行。
 */
export interface SuitePick {
  suiteId: string;
  revisionId?: string;
}

const UNPUBLISHED_HINT = '该评测集尚未发布，运行前会先发布为 v1。';

const suiteLabel = (suite: SuiteSummary) =>
  suite.active_revision_id
    ? `${suite.name}（v${suite.active_version_no ?? 0} · ${suite.active_case_count ?? 0} 个启用用例）`
    : `${suite.name}（未发布 · ${suite.draft_case_count ?? 0} 个用例）`;

/**
 * SuitePicker 受控组件：两级选择评测集及其 published 版本。value/onChange 兼容
 * antd Form.Item 直接作为受控子组件使用（value/onChange 语义一致）。内部所有异步
 * effect 只依赖稳定标量（resourceKind / selectedSuiteId），onChange 通过 ref 读取、
 * emit 幂等去重，避免 Form.Item 注入的 onChange 引用变化引发重载/设值循环。
 */
export const SuitePicker = ({ resourceKind, value, onChange, canManage, allowUnpublished, onNeedCreate }: {
  resourceKind: ResourceKind;
  value: SuitePick | null;
  onChange: (pick: SuitePick | null) => void;
  /** 空态时展示「去新建评测集」入口（admin）；供父层决定跳转行为，保持组件无导航副作用。 */
  canManage?: boolean;
  /** 允许选择尚无已发布版本的草稿套件（state B：发布 v1 后再运行）。 */
  allowUnpublished?: boolean;
  onNeedCreate?: () => void;
}) => {
  const [suites, setSuites] = useState<SuiteSummary[]>([]);
  const [versions, setVersions] = useState<SuiteRevisionMeta[]>([]);
  const [loadingSuites, setLoadingSuites] = useState(false);
  const [loadingVersions, setLoadingVersions] = useState(false);
  const [error, setError] = useState('');
  const [selectedSuiteId, setSelectedSuiteId] = useState('');

  const suitesRef = useRef(suites);
  suitesRef.current = suites;
  const onChangeRef = useRef(onChange);
  onChangeRef.current = onChange;
  // 每次 resourceKind 重载递增的请求代际，用于丢弃过期响应（实例级，多 picker 同屏不互扰）。
  const generationRef = useRef(0);
  // emit 按 (suiteId, revisionId) 幂等去重：父级（尤其 Form.Item 受控子组件）value
  // 已是目标值时不重复回调，避免 effect 依赖 onChange 引用变化造成设值环。
  const lastEmitKeyRef = useRef('');
  const emit = useCallback((pick: SuitePick | null) => {
    const key = pick ? `${pick.suiteId}|${pick.revisionId ?? ''}` : '';
    if (lastEmitKeyRef.current === key) return;
    lastEmitKeyRef.current = key;
    onChangeRef.current(pick);
  }, []);

  const published = useMemo(() => suites.filter((suite) => suite.active_revision_id), [suites]);
  const unpublished = useMemo(() => suites.filter((suite) => !suite.active_revision_id && suite.draft_revision_id), [suites]);

  // resourceKind 变化整体重载评测集并清空选择（只依赖稳定标量）。
  useEffect(() => {
    let cancelled = false;
    const generation = generationRef.current + 1;
    generationRef.current = generation;
    setSelectedSuiteId('');
    setVersions([]);
    emit(null);
    setLoadingSuites(true);
    setError('');
    evaluationApi.listSuites({ resource_kind: resourceKind })
      .then((page) => { if (!cancelled && generation === generationRef.current) setSuites(page.items); })
      .catch((err) => {
        if (cancelled || generation !== generationRef.current) return;
        setError(err instanceof Error ? err.message : '加载评测集失败');
      })
      .finally(() => { if (!cancelled && generation === generationRef.current) setLoadingSuites(false); });
    return () => { cancelled = true; };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- onChange 走 ref，避免引用变化触发重载
  }, [resourceKind]);

  const selectedSuite = useMemo(
    () => [...published, ...(allowUnpublished ? unpublished : [])].find((suite) => suite.id === selectedSuiteId),
    [published, unpublished, allowUnpublished, selectedSuiteId],
  );

  // 选中已发布套件后拉取 published 版本链并默认选 active（只依赖 selectedSuiteId）。
  useEffect(() => {
    const suite = suitesRef.current.find((item) => item.id === selectedSuiteId);
    if (!suite || !suite.active_revision_id) { setVersions([]); return; }
    let cancelled = false;
    setLoadingVersions(true);
    evaluationApi.listSuiteVersions(selectedSuiteId)
      .then((metas) => {
        if (cancelled) return;
        const publishedVersions = metas.filter((meta) => meta.status === 'published' && meta.version_no !== undefined);
        setVersions(publishedVersions);
        const preferred = publishedVersions.find((meta) => meta.id === suite.active_revision_id) ?? publishedVersions[0];
        if (preferred) emit({ suiteId: selectedSuiteId, revisionId: preferred.id });
      })
      .catch((err) => { if (!cancelled) setError(err instanceof Error ? err.message : '加载版本失败'); })
      .finally(() => { if (!cancelled) setLoadingVersions(false); });
    return () => { cancelled = true; };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- suites/emit 经 ref/稳定引用读取
  }, [selectedSuiteId]);

  const selectSuite = (suiteId: string) => {
    setSelectedSuiteId(suiteId);
    const suite = suitesRef.current.find((item) => item.id === suiteId);
    if (!suite) return emit(null);
    if (suite.active_revision_id) {
      // 已发布：版本链由上述 effect 加载后 emit 默认 active；此处先清 revision 防残留。
      emit({ suiteId });
    } else if (allowUnpublished) {
      emit({ suiteId });
    } else {
      emit(null);
    }
  };

  const groupOptions = useMemo(() => {
    const groups: { label: string; options: { value: string; label: string }[] }[] = [];
    if (published.length) groups.push({ label: '已发布评测集', options: published.map((suite) => ({
      value: suite.id, label: suiteLabel(suite),
    })) });
    if (allowUnpublished && unpublished.length) groups.push({ label: '未发布草稿套件', options: unpublished.map((suite) => ({
      value: suite.id, label: suiteLabel(suite),
    })) });
    return groups;
  }, [published, unpublished, allowUnpublished]);

  const versionOptions = versions.map((meta) => ({
    value: meta.id, label: `v${meta.version_no} · ${meta.enabled_case_count ?? 0} 个启用用例`,
  }));

  if (loadingSuites) return <Spin />;
  if (error) return <Alert type="error" showIcon message={error} action={<Button size="small"
    onClick={() => { /* resourceKind 重选即重载；错误信息已展示 */ }}>知道了</Button>} />;
  if (!suites.length) {
    return canManage
      ? <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="还没有可用于运行的评测集">
        <Button type="primary" onClick={() => onNeedCreate?.()}>新建评测集</Button>
      </Empty>
      : <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="当前资源类型下还没有评测集" />;
  }

  return <Space direction="vertical" style={{ width: '100%' }} size={8}>
    <Select aria-label="评测集" placeholder="选择评测集" value={selectedSuiteId || undefined} onChange={selectSuite}
      style={{ width: '100%' }} options={groupOptions}
      notFoundContent={allowUnpublished ? '暂无评测集' : '该资源类型暂无已发布评测集'} />
    {selectedSuite?.active_revision_id && (
      loadingVersions
        ? <Spin size="small" />
        : <Select aria-label="评测版本" placeholder="选择已发布版本" value={value?.revisionId}
          onChange={(revisionId) => emit({ suiteId: selectedSuiteId, revisionId })}
          style={{ width: '100%' }} options={versionOptions} notFoundContent="该评测集没有已发布版本" />
    )}
    {selectedSuite && !selectedSuite.active_revision_id && allowUnpublished && (
      <Alert type="warning" showIcon message={UNPUBLISHED_HINT} />
    )}
  </Space>;
};
