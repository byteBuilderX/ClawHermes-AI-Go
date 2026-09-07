import { Typography } from 'antd';

import ReviewPoolPanel from '../components/ReviewPoolPanel';

import { useAuth, useTenantRole } from '@/modules/iam';

export const ReviewPoolPage = () => {
  const { user } = useAuth();
  const { isOwner } = useTenantRole();
  const userId = user?.id;
  return <div>
    <Typography.Title level={4} style={{ margin: '0 0 4px' }}>人工评审池</Typography.Title>
    <Typography.Text type="secondary">审阅判定的分歧与低置信命中，记录人工决定</Typography.Text>
    <div style={{ marginTop: 12 }}>
      <ReviewPoolPanel canDelete={(item) => isOwner || (!!item.created_by && item.created_by === userId)} />
    </div>
  </div>;
};
