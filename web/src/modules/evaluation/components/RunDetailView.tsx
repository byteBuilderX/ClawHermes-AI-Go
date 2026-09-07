import { Alert, Descriptions, Progress, Skeleton, Tabs, Typography } from 'antd';
import { useEffect, useState } from 'react';

import { evaluationApi } from '../api/evaluation.api';
import type { EvaluationRun, RunResourceAnchor, RunSummary } from '../model/evaluation';

import { CompareRunsPanel } from './CompareRunsPanel';
import { RunAnchorPanel } from './RunAnchorPanel';
import { RunAttributionPanel } from './RunAttributionPanel';
import { RunMetricPanel } from './RunMetricPanel';
import { runDisplayStatus, StatusTag } from './evaluationView';

// 运行详情的展示体：自取 getRun 详情，挂载于运行详情页（RunDetailPage）。
export const RunDetailView = ({ run, runs, isMobile }: {
  run: RunSummary; runs: RunSummary[]; isMobile?: boolean;
}) => {
  const [metrics, setMetrics] = useState<Record<string, unknown> | null>(null);
  const [runResults, setRunResults] = useState<EvaluationRun['results'] | null>(null);
  const [anchors, setAnchors] = useState<RunResourceAnchor[] | null>(null);

  useEffect(() => {
    let cancelled = false;
    setMetrics(null);
    setRunResults(null);
    setAnchors(null);
    void evaluationApi.getRun(run.id)
      .then((detail) => {
        if (!cancelled) {
          setMetrics(detail.metrics ?? {});
          setRunResults(detail.results ?? []);
          setAnchors(detail.anchors ?? []);
        }
      })
      .catch(() => {
        if (!cancelled) {
          setMetrics({});
          setRunResults([]);
          setAnchors([]);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [run.id]);

  return <Tabs
    items={[
      {
        key: 'facts',
        label: '观测事实',
        children: <>
          <Typography.Title level={5}>观测事实</Typography.Title>
          <Descriptions bordered size="small" column={isMobile ? 1 : 2}>
            <Descriptions.Item label="运行状态"><StatusTag value={runDisplayStatus(run.status, run.passed)} /></Descriptions.Item>
            <Descriptions.Item label="资源版本">{run.revision_id}</Descriptions.Item>
            <Descriptions.Item label="通过用例">{run.passed_cases} / {run.total_cases}</Descriptions.Item>
            <Descriptions.Item label="创建时间">{new Date(run.created_at).toLocaleString('zh-CN')}</Descriptions.Item>
          </Descriptions>
          <Progress percent={run.total_cases ? Math.round(run.passed_cases / run.total_cases * 100) : 0} />
          {!run.passed && <Alert type="warning" showIcon message="运行未通过，请依据已脱敏的失败摘要定位问题。" />}
          <RunAnchorPanel anchors={anchors} />
        </>,
      },
      {
        key: 'metrics',
        label: '指标',
        children: metrics === null
          ? <Skeleton active paragraph={{ rows: 4 }} />
          : <RunMetricPanel metrics={metrics} />,
      },
      {
        key: 'attribution',
        label: '归因',
        children: runResults === null
          ? <Skeleton active paragraph={{ rows: 4 }} />
          : <RunAttributionPanel results={runResults} />,
      },
      {
        key: 'compare',
        label: '版本对比',
        children: <CompareRunsPanel currentId={run.id} runs={runs} getRun={evaluationApi.getRun} />,
      },
    ]}
  />;
};
