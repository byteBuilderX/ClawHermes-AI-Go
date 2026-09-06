import { render, screen } from '@testing-library/react';
import { MemoryRouter, Routes } from 'react-router-dom';
import { describe, expect, it, vi } from 'vitest';

import { evaluationRoutes } from './routes';

vi.mock('@/modules/iam', () => ({ PrivateRoute: ({ children }: { children: React.ReactNode }) => children }));
vi.mock('./pages/EvaluationCenterPage', () => ({ EvaluationCenterPage: () => <div>评测中心路由</div> }));
vi.mock('./pages/SuiteListPage', () => ({ SuiteListPage: () => <div>评测集列表路由</div> }));
vi.mock('./pages/SuiteDetailPage', () => ({ SuiteDetailPage: () => <div>评测集详情路由</div> }));

describe('evaluation routes', () => {
  it('registers the private evaluations route', () => {
    render(
      <MemoryRouter initialEntries={['/evaluations']}>
        <Routes>{evaluationRoutes}</Routes>
      </MemoryRouter>,
    );
    expect(screen.getByText('评测中心路由')).toBeInTheDocument();
  });

  it('registers the standalone suite list route under /evaluations/suites', () => {
    render(
      <MemoryRouter initialEntries={['/evaluations/suites']}>
        <Routes>{evaluationRoutes}</Routes>
      </MemoryRouter>,
    );
    expect(screen.getByText('评测集列表路由')).toBeInTheDocument();
  });

  it('registers the suite detail route with a captured :id param', () => {
    render(
      <MemoryRouter initialEntries={['/evaluations/suites/suite-1']}>
        <Routes>{evaluationRoutes}</Routes>
      </MemoryRouter>,
    );
    expect(screen.getByText('评测集详情路由')).toBeInTheDocument();
  });
});
