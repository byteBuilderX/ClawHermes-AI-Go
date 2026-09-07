import { Navigate, Route } from 'react-router-dom';

import { EvolutionPage } from './pages/EvolutionPage';
import { ObservabilityPage } from './pages/ObservabilityPage';
import { ResourceDetailPage } from './pages/ResourceDetailPage';
import { ResourceListPage } from './pages/ResourceListPage';
import { ReviewPoolPage } from './pages/ReviewPoolPage';
import { RunDetailPage } from './pages/RunDetailPage';
import { RunListPage } from './pages/RunListPage';
import { SuiteDetailPage } from './pages/SuiteDetailPage';
import { SuiteListPage } from './pages/SuiteListPage';

import { PrivateRoute } from '@/modules/iam';

export const evaluationRoutes = [
  // /evaluations 首页（原「评测与进化中心」hub）已拆除，能力下沉到子页；
  // 裸路径 SPA 重定向到离线运行页，避免旧书签/外链 404。
  <Route
    key="evaluations"
    path="/evaluations"
    element={
      <PrivateRoute>
        <Navigate to="/evaluations/runs" replace />
      </PrivateRoute>
    }
  />,
  <Route
    key="evaluations-runs"
    path="/evaluations/runs"
    element={
      <PrivateRoute>
        <RunListPage />
      </PrivateRoute>
    }
  />,
  <Route
    key="evaluations-run-detail"
    path="/evaluations/runs/:runId"
    element={
      <PrivateRoute>
        <RunDetailPage />
      </PrivateRoute>
    }
  />,
  <Route
    key="evaluations-evolution"
    path="/evaluations/evolution"
    element={
      <PrivateRoute>
        <EvolutionPage />
      </PrivateRoute>
    }
  />,
  <Route
    key="evaluations-resources"
    path="/evaluations/resources"
    element={
      <PrivateRoute>
        <ResourceListPage />
      </PrivateRoute>
    }
  />,
  <Route
    key="evaluations-resource-detail"
    path="/evaluations/resources/:kind/:id"
    element={
      <PrivateRoute>
        <ResourceDetailPage />
      </PrivateRoute>
    }
  />,
  <Route
    key="evaluations-observability"
    path="/evaluations/observability"
    element={
      <PrivateRoute>
        <ObservabilityPage />
      </PrivateRoute>
    }
  />,
  <Route
    key="evaluations-review"
    path="/evaluations/review"
    element={
      <PrivateRoute>
        <ReviewPoolPage />
      </PrivateRoute>
    }
  />,
  <Route
    key="evaluations-suites"
    path="/evaluations/suites"
    element={
      <PrivateRoute>
        <SuiteListPage />
      </PrivateRoute>
    }
  />,
  <Route
    key="evaluations-suite-detail"
    path="/evaluations/suites/:id"
    element={
      <PrivateRoute>
        <SuiteDetailPage />
      </PrivateRoute>
    }
  />,
];
