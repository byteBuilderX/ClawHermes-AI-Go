import { FileTextOutlined } from '@ant-design/icons';
import { Badge, Space, Typography } from 'antd';
import { useState } from 'react';

import type { ChatCitationSource } from '../model/agent';

import { DocPreviewDrawer } from '@/modules/knowledge';

const { Text, Paragraph } = Typography;

// SourceCardList 渲染一条回答引用的来源文档卡片（P1.4）。点击卡片打开
// DocPreviewDrawer 预览原文，员工可核验回答依据。
interface SourceCardListProps {
  sources?: ChatCitationSource[];
}

const MAX_CARDS = 8;

const renderScore = (s: ChatCitationSource) => {
  if (!s.hasScore || typeof s.score !== 'number') return null;
  return (
    <Badge
      count={`${(s.score * 100).toFixed(1)}%`}
      style={{ background: '#52c41a', fontSize: 11 }}
    />
  );
};

export const SourceCardList = ({ sources }: SourceCardListProps) => {
  const list = (sources ?? []).slice(0, MAX_CARDS);
  const [preview, setPreview] = useState<{ name: string; documentID: string; title: string } | null>(null);

  if (list.length === 0) return null;

  return (
    <div style={{ marginTop: 12 }}>
      <Text strong style={{ fontSize: 12, display: 'block', marginBottom: 6 }}>
        来源文档（{list.length}）
      </Text>
      <Space direction="vertical" style={{ width: '100%' }} size={6}>
        {list.map((s, i) => {
          const title = s.documentTitle || s.documentId || '未知文档';
          const workspaceName = s.workspaceName || '';
          const previewable = Boolean(workspaceName && s.documentId);
          return (
            <div
              key={s.chunkId || s.documentId || i}
              onClick={
                previewable
                  ? () => setPreview({ name: workspaceName, documentID: s.documentId!, title })
                  : undefined
              }
              style={{
                background: '#fafafa',
                border: '1px solid #f0f0f0',
                borderRadius: 8,
                padding: '8px 12px',
                cursor: previewable ? 'pointer' : 'default',
                transition: 'border-color 0.2s',
              }}
              onMouseEnter={(e) => {
                if (previewable) e.currentTarget.style.borderColor = '#2563eb';
              }}
              onMouseLeave={(e) => {
                e.currentTarget.style.borderColor = '#f0f0f0';
              }}
            >
              <Space size={8} style={{ marginBottom: 4, width: '100%' }} align="center">
                <FileTextOutlined style={{ color: '#2563eb' }} />
                <Text strong ellipsis style={{ flex: 1, fontSize: 13 }}>
                  {title}
                </Text>
                {renderScore(s)}
              </Space>
              {s.snippet && (
                <Paragraph
                  ellipsis={{ rows: 2 }}
                  type="secondary"
                  style={{ margin: 0, fontSize: 12, color: '#8c8c8c' }}
                >
                  {s.snippet}
                </Paragraph>
              )}
            </div>
          );
        })}
      </Space>
      <DocPreviewDrawer
        open={Boolean(preview)}
        name={preview?.name ?? ''}
        documentID={preview?.documentID ?? ''}
        documentTitle={preview?.title}
        onClose={() => setPreview(null)}
      />
    </div>
  );
};

export default SourceCardList;
