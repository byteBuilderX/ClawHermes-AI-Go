import { CaretRightOutlined } from '@ant-design/icons';
import { Collapse, Typography } from 'antd';

const { Text, Paragraph } = Typography;

export interface ParentContextBlockProps {
  content: string;
}

// ParentContextBlock：引用卡片就地展示命中 chunk 所在整节原文（Parent-Child
// chunking 的 parent；其它策略该字段为空、组件不渲染）。灰块样式与
// DocPreviewDrawer 中 parent 段一致，保证"就地看上下文"与"整篇预览"视觉同源。
// content 保留原始换行（whiteSpace: pre-wrap）。
//
// 整个触发区统一 stopPropagation：父级卡片若整卡可点开 DocPreviewDrawer
// （SourceCardList 卡片 div 的 onClick），展开 parent 不能同时触发预览抽屉；
// 触发区外的文本选择等操作不受影响（stopPropagation 只挡 click 冒泡）。
export const ParentContextBlock = ({ content }: ParentContextBlockProps) => {
  if (!content) return null;

  return (
    <div onClick={(e) => e.stopPropagation()}>
      <Collapse
        size="small"
        ghost
        expandIcon={({ isActive }) => (
          <CaretRightOutlined
            rotate={isActive ? 90 : 0}
            style={{ fontSize: 11, color: '#8c8c8c' }}
          />
        )}
        items={[
          {
            key: 'context',
            label: (
              <Text type="secondary" style={{ fontSize: 12 }}>
                查看上下文
              </Text>
            ),
            children: (
              <Paragraph
                type="secondary"
                className="long-text"
                style={{
                  margin: 0,
                  padding: '8px 10px',
                  background: '#fafafa',
                  borderLeft: '3px solid #d9d9d9',
                  fontSize: 12,
                  lineHeight: 1.6,
                  whiteSpace: 'pre-wrap',
                }}
              >
                {content}
              </Paragraph>
            ),
          },
        ]}
      />
    </div>
  );
};

export default ParentContextBlock;
