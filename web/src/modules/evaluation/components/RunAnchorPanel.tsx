import { Descriptions, Skeleton, Tag, Typography } from 'antd';
import { Link } from 'react-router-dom';

import type { RunResourceAnchor } from '../model/evaluation';

import { displayLabel } from './evaluationView';

// RunAnchorPanel 把 run 详情里评测锚定的资源版本清单显式展示出来：被测主体恒在
// 首位（run 创建时点锁定的被测 revision），其后是绑定的 skill/mcp/knowledge 及
// 各自当时生效的版本 pin。skill 资源可跳到其 workspace 查看版本历史；仅含被测
// 主体的历史 run 提示未记录绑定资源锚定。anchors 为 null 表示详情仍在加载。
export const RunAnchorPanel = ({ anchors }: { anchors: RunResourceAnchor[] | null }) => {
  const items = anchors ?? [];
  return (
    <div>
      <Typography.Title level={5}>评测资源版本锚定</Typography.Title>
      {anchors === null ? (
        <Skeleton active paragraph={{ rows: 2 }} />
      ) : (
        <>
          <Descriptions bordered size="small" column={1}>
            {items.map((item, index) => (
              <Descriptions.Item
                key={`${item.kind}:${item.resource_id}`}
                label={index === 0 ? (
                  <span><Tag color="blue">被测</Tag>{displayLabel(item.kind)}</span>
                ) : displayLabel(item.kind)}
              >
                {item.kind === 'skill' ? (
                  <Link to={`/skills/${item.resource_id}/workspace`}>{item.resource_id}</Link>
                ) : (
                  <Typography.Text code>{item.resource_id}</Typography.Text>
                )}
                {' '}
                <Typography.Text type="secondary">版本</Typography.Text>
                {' '}
                <Typography.Text code>{item.revision_id}</Typography.Text>
              </Descriptions.Item>
            ))}
          </Descriptions>
          {items.length <= 1 && (
            <Typography.Paragraph type="secondary" style={{ marginTop: 8 }}>
              该 run 未记录绑定资源（skill / MCP / 知识库）的版本锚定，仅能确认被测资源版本。
            </Typography.Paragraph>
          )}
        </>
      )}
    </div>
  );
};
