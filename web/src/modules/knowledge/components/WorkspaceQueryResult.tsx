import { Alert, Badge, Divider, Space, Tag, Typography } from 'antd';
import { useState } from 'react';

import type {
  NoAnswerReason,
  ParsedNoAnswerInfo,
  QueryResult,
  QuerySource,
} from '../model/knowledge';

import { DocPreviewDrawer } from './DocPreviewDrawer';

import { ParentContextBlock } from '@/shared/ui';

const { Text, Paragraph } = Typography;

// 拒答文案 map：reason 固定枚举（后端 pkg/constants 单一事实源），文案只
// 允许固定模板。与 agent 侧 NoAnswerNotice 平行定义，避免跨模块 import。
const REASON_TEXT: Record<NoAnswerReason, string> = {
  no_sources: '知识库中未检索到相关内容，未基于知识库作答。',
  threshold_filtered: '检索到的候选均未达到相关性阈值，未基于知识库作答。',
  access_restricted: '当前身份在知识库中无可见文档，无法基于知识库作答。',
  insufficient_evidence: '检索到的证据不足以支撑回答，未基于知识库作答。',
  unsupported_mode: '当前检索模式不被支持，未基于知识库作答。',
};

// 拒答提示：替代绿色回答卡，向用户解释为什么没有答案。
// detail/统计均为固定模板拼装，不携带检索原文。
const RefusalNotice = ({ noAnswer }: { noAnswer: ParsedNoAnswerInfo }) => {
  const text = REASON_TEXT[noAnswer.reason] ?? REASON_TEXT.no_sources;
  const parts: string[] = [];
  if (noAnswer.retrieved_count > 0) parts.push(`检索到 ${noAnswer.retrieved_count} 条候选`);
  if (noAnswer.filtered_count > 0) parts.push(`阈值过滤 ${noAnswer.filtered_count} 条`);
  if (noAnswer.best_score > 0) parts.push(`最高相关度 ${(noAnswer.best_score * 100).toFixed(1)}%`);
  if (noAnswer.retried && noAnswer.rewritten_query) {
    parts.push(`已改写查询重试：${noAnswer.rewritten_query}`);
  }
  if (noAnswer.detail) parts.push(noAnswer.detail);
  return (
    <Alert
      type="info"
      showIcon
      message={text}
      description={parts.length > 0 ? parts.join('；') : undefined}
      style={{ marginBottom: 12 }}
    />
  );
};

interface WorkspaceQueryResultProps {
  result: QueryResult;
}

// 文档名可点击打开原文预览（P1.4）；workspace/document_title 由 P1.1 的
// query 响应携带，缺失（旧后端）时退化为不可点击的截断 id。
const SourceItem = ({ source }: { source: QuerySource }) => {
  const [previewOpen, setPreviewOpen] = useState(false);
  const title = source.document_title || source.document_id || '';
  const previewable = Boolean(source.workspace && source.document_id);

  return (
    <div
      className="long-text"
      style={{
        background: '#fafafa',
        border: '1px solid #f0f0f0',
        padding: '10px 14px',
      }}
    >
      <Space size={8} style={{ marginBottom: 6 }}>
        <Tag
          style={{ margin: 0, cursor: previewable ? 'pointer' : 'default' }}
          color={previewable ? 'blue' : undefined}
          onClick={previewable ? () => setPreviewOpen(true) : undefined}
        >
          文档: {title.slice(0, 32) || '-'}
        </Tag>
        <Badge
          count={`${((source.score ?? 0) * 100).toFixed(1)}%`}
          style={{ background: '#52c41a', fontSize: 11 }}
        />
      </Space>
      <Paragraph
        ellipsis={{ rows: 2 }}
        type="secondary"
        style={{ margin: 0, fontSize: 13 }}
      >
        {source.content}
      </Paragraph>
      {/* 命中 chunk 所在整节原文(Parent-Child 策略才有值)就地展开看上下文 */}
      {source.parent_content && <ParentContextBlock content={source.parent_content} />}
      {previewable && (
        <DocPreviewDrawer
          open={previewOpen}
          name={source.workspace!}
          documentID={source.document_id!}
          documentTitle={source.document_title}
          onClose={() => setPreviewOpen(false)}
        />
      )}
    </div>
  );
};

export const WorkspaceQueryResult = ({ result }: WorkspaceQueryResultProps) => (
  <>
    <Divider style={{ margin: '0 0 16px' }} />
    {/* no_answer 存在 → 拒答提示替换绿色回答卡；null/undefined（旧后端或
        正常回答）走原回答卡，nullish 判定兼容滚动升级 */}
    {result.no_answer ? (
      <RefusalNotice noAnswer={result.no_answer} />
    ) : (
      <div
        style={{
          background: '#f6ffed',
          border: '1px solid #b7eb8f',
          // 结果卡片语义，与 token.borderRadiusLG 对齐
          borderRadius: 12,
          padding: 16,
          marginBottom: 12,
        }}
      >
        <Text
          strong
          style={{ display: 'block', marginBottom: 8, fontSize: 13, color: '#52c41a' }}
        >
          回答
        </Text>
        <Paragraph className="long-text" style={{ margin: 0, lineHeight: 1.7 }}>{result.answer}</Paragraph>
      </div>
    )}
    {result.sources && result.sources.length > 0 && (
      <div>
        <Text strong style={{ fontSize: 13, display: 'block', marginBottom: 8 }}>
          来源文档（{result.sources.length}）
        </Text>
        <Space direction="vertical" style={{ width: '100%' }} size={8}>
          {result.sources.map((s, i) => (
            <SourceItem key={s.document_id || i} source={s} />
          ))}
        </Space>
      </div>
    )}
  </>
);
