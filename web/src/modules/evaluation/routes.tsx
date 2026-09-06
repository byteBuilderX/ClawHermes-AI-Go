import { Route } from 'react-router-dom';

import { EvaluationCenterPage } from './pages/EvaluationCenterPage';
import { SuiteDetailPage } from './pages/SuiteDetailPage';
import { SuiteListPage } from './pages/SuiteListPage';

import { PrivateRoute } from '@/modules/iam';

export const evaluationRoutes = [
  <Route
    key="evaluations"
    path="/evaluations"
    element={
      <PrivateRoute>
        <EvaluationCenterPage />
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
