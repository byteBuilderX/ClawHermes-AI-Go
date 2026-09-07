import { Drawer } from 'antd';

import type { TimelineEvent } from '../model/evaluation';

import { TimelinePanel } from './TimelinePanel';
import { drawerWidth } from './evaluationView';

// 资源时间线的抽屉壳：内容统一委托 TimelinePanel（资源详情页直接内嵌该面板）。
export const TimelineDrawer = ({ events, open, onClose, loading, error, isMobile }: {
  events: TimelineEvent[]; open: boolean; onClose: () => void; loading?: boolean; error?: string; isMobile?: boolean;
}) => (
  <Drawer title="资源时间线" open={open} onClose={onClose} width={drawerWidth(isMobile)} destroyOnHidden>
    <TimelinePanel events={events} loading={loading} error={error} />
  </Drawer>
);
