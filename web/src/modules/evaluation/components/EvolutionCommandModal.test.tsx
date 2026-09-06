import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { EvolutionCommandModal } from './EvolutionCommandModal';

const api = vi.hoisted(() => ({ listSuites: vi.fn(), listSuiteVersions: vi.fn() }));
vi.mock('../api/evaluation.api', () => ({ evaluationApi: api }));
const listSuites = api.listSuites;
const listSuiteVersions = api.listSuiteVersions;

const publishedSuite = {
  id: 'suite-1', name: '投诉基线', description: '', status: 'published', resource_kind: 'skill',
  active_revision_id: 'rev-v2', draft_revision_id: 'rev-draft', active_version_no: 2, active_case_count: 5,
  created_by: 'u1', created_at: '2026-09-01T00:00:00Z',
};
const publishedVersions = [
  { id: 'rev-v1', version_no: 1, status: 'published', resource_kind: 'skill', enabled_case_count: 4 },
  { id: 'rev-v2', version_no: 2, status: 'published', resource_kind: 'skill', enabled_case_count: 5 },
];

const tabPanel = (tabName: string) => {
  const tab = screen.getByRole('tab', { name: tabName });
  const panelId = tab.getAttribute('aria-controls');
  const panel = panelId ? document.getElementById(panelId) : null;
  if (!panel) throw new Error(`tabpanel not found for tab: ${tabName}`);
  return panel;
};

const chooseResourceKind = async (panel: HTMLElement, kindLabel: string) => {
  fireEvent.mouseDown(within(panel).getByRole('combobox', { name: '资源类型' }));
  fireEvent.click(await screen.findByText(kindLabel));
};

const choosePublishedSuite = async (panel: HTMLElement) => {
  fireEvent.mouseDown(within(panel).getByRole('combobox', { name: '评测集' }));
  fireEvent.click(await screen.findByText('投诉基线（v2 · 5 个启用用例）'));
  await waitFor(() => expect(listSuiteVersions).toHaveBeenCalledWith('suite-1'));
};

describe('EvolutionCommandModal', () => {
  beforeEach(() => {
    listSuites.mockReset();
    listSuiteVersions.mockReset();
  });

  it('disables the optimization submit and prompts until a resource kind is chosen', () => {
    render(<EvolutionCommandModal open onClose={vi.fn()} onOptimize={vi.fn()}
      onExperiment={vi.fn()} onFeedback={vi.fn()} />);
    const panel = within(tabPanel('生成优化候选'));

    expect(panel.getByText('请先选择资源类型，再选择已发布评测套件。')).toBeInTheDocument();
    expect(panel.getByRole('button', { name: '生成候选' })).toBeDisabled();
    expect(panel.queryByRole('combobox', { name: '评测集' })).not.toBeInTheDocument();
  });

  it('submits the optimization command with the chosen published suite revision', async () => {
    listSuites.mockResolvedValue({ items: [publishedSuite] });
    listSuiteVersions.mockResolvedValue(publishedVersions);
    const onClose = vi.fn();
    const onOptimize = vi.fn().mockResolvedValue(undefined);
    render(<EvolutionCommandModal open onClose={onClose} onOptimize={onOptimize}
      onExperiment={vi.fn()} onFeedback={vi.fn()} />);
    const optimizationPanel = tabPanel('生成优化候选');
    const panel = within(optimizationPanel);

    await chooseResourceKind(optimizationPanel, 'Agent');
    await waitFor(() => expect(listSuites).toHaveBeenCalledWith({ resource_kind: 'agent' }));
    fireEvent.change(panel.getByLabelText('资源 ID'), { target: { value: 'agent-1' } });
    fireEvent.change(panel.getByLabelText('稳定 Revision ID'), { target: { value: 'stable-1' } });
    fireEvent.change(panel.getByLabelText('失败摘要'), { target: { value: '幻觉导致答错' } });
    await choosePublishedSuite(optimizationPanel);

    const submit = panel.getByRole('button', { name: '生成候选' });
    await waitFor(() => expect(submit).toBeEnabled());
    fireEvent.click(submit);

    await waitFor(() => expect(onOptimize).toHaveBeenCalledWith(expect.objectContaining({
      resource_kind: 'agent', resource_id: 'agent-1', stable_revision_id: 'stable-1',
      failure_summary: '幻觉导致答错', suite_revision_id: 'rev-v2',
    })));
    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });

  it('submits the experiment command with the chosen published suite revision', async () => {
    listSuites.mockResolvedValue({ items: [publishedSuite] });
    listSuiteVersions.mockResolvedValue(publishedVersions);
    const onExperiment = vi.fn().mockResolvedValue(undefined);
    render(<EvolutionCommandModal open onClose={vi.fn()} onOptimize={vi.fn()}
      onExperiment={onExperiment} onFeedback={vi.fn()} />);
    fireEvent.click(screen.getByRole('tab', { name: '创建金丝雀' }));
    const experimentPanel = tabPanel('创建金丝雀');
    const panel = within(experimentPanel);

    await chooseResourceKind(experimentPanel, 'Agent');
    await waitFor(() => expect(listSuites).toHaveBeenCalledWith({ resource_kind: 'agent' }));
    fireEvent.change(panel.getByLabelText('资源 ID'), { target: { value: 'agent-1' } });
    fireEvent.change(panel.getByLabelText('稳定 Revision ID'), { target: { value: 'stable-1' } });
    fireEvent.change(panel.getByLabelText('候选 Revision ID'), { target: { value: 'candidate-1' } });
    await choosePublishedSuite(experimentPanel);

    const submit = panel.getByRole('button', { name: '创建金丝雀' });
    await waitFor(() => expect(submit).toBeEnabled());
    fireEvent.click(submit);

    await waitFor(() => expect(onExperiment).toHaveBeenCalledWith(expect.objectContaining({
      resource_kind: 'agent', resource_id: 'agent-1', stable_revision_id: 'stable-1',
      candidate_revision_id: 'candidate-1', suite_revision_id: 'rev-v2',
    })));
  });

  it('keeps the feedback tab free of suite fields', async () => {
    const onFeedback = vi.fn().mockResolvedValue(undefined);
    render(<EvolutionCommandModal open onClose={vi.fn()} onOptimize={vi.fn()}
      onExperiment={vi.fn()} onFeedback={onFeedback} />);
    fireEvent.click(screen.getByRole('tab', { name: '记录反馈' }));
    const panel = within(tabPanel('记录反馈'));

    expect(screen.queryByRole('combobox', { name: '评测集' })).not.toBeInTheDocument();
    fireEvent.change(panel.getByLabelText('Trace ID'), { target: { value: 'trace-1' } });
    fireEvent.change(panel.getByLabelText('反馈资源 ID'), { target: { value: 'skill-1' } });
    fireEvent.change(panel.getByLabelText('分数'), { target: { value: '0.5' } });
    fireEvent.click(panel.getByRole('button', { name: '提交反馈' }));

    await waitFor(() => expect(onFeedback).toHaveBeenCalledWith(expect.objectContaining({
      trace_id: 'trace-1', resource_id: 'skill-1', score: 0.5,
    })));
    const payload = onFeedback.mock.calls[0][0] as Record<string, unknown>;
    expect(payload).not.toHaveProperty('suite_revision_id');
  });
});
