import { render, screen } from '@testing-library/react';
import { MemoryRouter, Routes } from 'react-router-dom';
import { describe, expect, it, vi } from 'vitest';

import { evaluationRoutes } from './routes';

vi.mock('@/modules/iam', () => ({ PrivateRoute: ({ children }: { children: React.ReactNode }) => children }));
vi.mock('./pages/EvaluationCenterPage', () => ({ EvaluationCenterPage: () => <div>评测中心路由</div> }));
vi.mock('./pages/SuiteListPage', () => ({ SuiteListPage: () => <div>评测集列表路由</div> }));
vi.mock('./pages/SuiteDetailPage', () => ({ SuiteDetailPage: () => <div>评测集详情路由</div> }));
vi.mock('./pages/RunListPage', () => ({ RunListPage: () => <div>离线运行列表路由</div> }));
vi.mock('./pages/RunDetailPage', () => ({ RunDetailPage: () => <div>离线运行详情路由</div> }));
vi.mock('./pages/ResourceDetailPage', () => ({ ResourceDetailPage: () => <div>被测资源详情路由</div> }));
vi.mock('./pages/EvolutionPage', () => ({ EvolutionPage: () => <div>自进化工作区路由</div> }));
vi.mock('./pages/ResourceListPage', () => ({ ResourceListPage: () => <div>被测资源列表路由</div> }));
vi.mock('./pages/ObservabilityPage', () => ({ ObservabilityPage: () => <div>在线观测路由</div> }));
vi.mock('./pages/ReviewPoolPage', () => ({ ReviewPoolPage: () => <div>人工评审池路由</div> }));

const cases: Array<{ entry: string; label: string }> = [
  { entry: '/evaluations', label: '评测中心路由' },
  { entry: '/evaluations/runs', label: '离线运行列表路由' },
  { entry: '/evaluations/runs/run-1', label: '离线运行详情路由' },
  { entry: '/evaluations/evolution', label: '自进化工作区路由' },
  { entry: '/evaluations/resources', label: '被测资源列表路由' },
  { entry: '/evaluations/resources/agent/agent-1', label: '被测资源详情路由' },
  { entry: '/evaluations/observability', label: '在线观测路由' },
  { entry: '/evaluations/review', label: '人工评审池路由' },
  { entry: '/evaluations/suites', label: '评测集列表路由' },
  { entry: '/evaluations/suites/suite-1', label: '评测集详情路由' },
];

describe('evaluation routes', () => {
  it.each(cases)('registers the private route for $entry', ({ entry, label }) => {
    render(
      <MemoryRouter initialEntries={[entry]}>
        <Routes>{evaluationRoutes}</Routes>
      </MemoryRouter>,
    );
    expect(screen.getByText(label)).toBeInTheDocument();
  });
});
