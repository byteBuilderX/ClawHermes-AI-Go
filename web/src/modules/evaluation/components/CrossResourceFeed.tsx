import { Button, Empty, List, Tag, Typography } from 'antd';
import { useMemo } from 'react';

import type { CandidateSummary, ExperimentSummary, RunSummary } from '../model/evaluation';

import { StatusTag, displayLabel, runDisplayStatus } from './evaluationView';

// 记录簿 feed 的归一化条目：把运行/候选/实验三类记录拍平为同构行，供中心页并集展示。
export type FeedEntry = {
  key: string;
  id: string;
  kind: 'run' | 'candidate' | 'experiment';
  resourceId: string;
  resourceKind: string;
  revision: string;
  status: string;
  passed: boolean;
  createdAt: string;
  detail: string;
};

const KIND_LABEL: Record<FeedEntry['kind'], string> = { run: '运行', candidate: '候选', experiment: '实验' };

const runEntry = (run: RunSummary): FeedEntry => ({
  key: `run:${run.id}`, id: run.id, kind: 'run', resourceId: run.resource_id, resourceKind: run.resource_kind,
  revision: run.revision_id, status: run.status, passed: run.passed, createdAt: run.created_at,
  detail: `通过 ${run.passed_cases}/${run.total_cases} · ${run.id}`,
});
const candidateEntry = (candidate: CandidateSummary): FeedEntry => ({
  key: `candidate:${candidate.id}`, id: candidate.id, kind: 'candidate', resourceId: candidate.resource_id,
  resourceKind: candidate.resource_kind, revision: candidate.revision_id, status: candidate.status, passed: false,
  createdAt: candidate.created_at, detail: `候选 ${candidate.revision_id} · ${candidate.source}`,
});
const experimentEntry = (experiment: ExperimentSummary): FeedEntry => ({
  key: `experiment:${experiment.id}`, id: experiment.id, kind: 'experiment', resourceId: experiment.resource_id,
  resourceKind: experiment.resource_kind, revision: experiment.canary_revision_id, status: experiment.status, passed: false,
  createdAt: experiment.created_at, detail: `稳定 ${experiment.stable_revision_id} → 候选 ${experiment.canary_revision_id} · ${experiment.stage_percent}%`,
});

// 纯函数：把三份首屏记录按 created_at 倒序归并（跨资源记录簿视图的 MVP 并集）。
export const buildFeedEntries = (
  runs: RunSummary[], candidates: CandidateSummary[], experiments: ExperimentSummary[],
): FeedEntry[] => {
  const entries: FeedEntry[] = [
    ...runs.map(runEntry), ...candidates.map(candidateEntry), ...experiments.map(experimentEntry),
  ];
  return entries.sort((a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt));
};

const statusLabel = (entry: FeedEntry) => (
  entry.kind === 'run' ? runDisplayStatus(entry.status, entry.passed) : entry.status
);

// 中心 hub 的记录簿时间线：跨资源展示最近运行/候选/实验，点「查看」跳对应链路页。
export const CrossResourceFeed = ({ runs, candidates, experiments, loading, onOpen }: {
  runs: RunSummary[]; candidates: CandidateSummary[]; experiments: ExperimentSummary[]; loading?: boolean;
  onOpen: (entry: FeedEntry) => void;
}) => {
  const entries = useMemo(
    () => buildFeedEntries(runs, candidates, experiments).slice(0, 20),
    [runs, candidates, experiments],
  );
  return <List<FeedEntry> size="small" loading={loading} dataSource={entries}
    locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="还没有评测活动记录" /> }}
    renderItem={(entry) => (
      <List.Item actions={[<Button key="open" type="link" size="small" onClick={() => onOpen(entry)}>查看</Button>]}>
        <List.Item.Meta
          title={<>{entry.detail}
            <Typography.Text type="secondary"> · {displayLabel(entry.resourceKind)} {entry.resourceId}</Typography.Text>
            <Tag style={{ marginLeft: 8 }}>{KIND_LABEL[entry.kind]}</Tag>
            <StatusTag value={statusLabel(entry)} />
          </>}
          description={<Typography.Text type="secondary">
            {new Date(entry.createdAt).toLocaleString('zh-CN')}
          </Typography.Text>}
        />
      </List.Item>
    )}
  />;
};
