import { Alert, Button, Collapse, Flex, Space, Tag, Typography } from 'antd';

import type { EvaluationCase } from '../model/evaluation';

import { SafeValue, displayLabel } from './evaluationView';

const modeTag = (mode: string) => <Tag color="blue">{displayLabel(mode)}</Tag>;

// toolSpecSummary 渲染工具序列确定性断言（§6.5）的紧凑摘要：必调用/禁调用/
// 顺序/上限；全空时返回 '—'。
const toolSpecSummary = (spec: NonNullable<EvaluationCase['tool_spec']>) => {
  const parts: string[] = [];
  if (spec.must_call?.length) parts.push(`必调用:${spec.must_call.join('/')}`);
  if (spec.must_not_call?.length) parts.push(`禁调用:${spec.must_not_call.join('/')}`);
  if (spec.order?.length) parts.push(`顺序:${spec.order.join('>')}`);
  if (spec.max_calls) parts.push(`上限:${spec.max_calls}`);
  return parts.join('；') || '—';
};

// provenanceOf 列出自动生成 case 的来源证据：生产 trace、反馈引用与生成原因；
// 全部缺省时返回 null（调用方据此决定是否渲染「生成来源」块）。
const provenanceOf = (testCase: EvaluationCase) => {
  const rows: Array<[string, string]> = [];
  if (testCase.source_trace_id) rows.push(['Trace', testCase.source_trace_id]);
  if (testCase.feedback_ref) rows.push(['反馈', testCase.feedback_ref]);
  if (testCase.generate_reason) rows.push(['生成原因', testCase.generate_reason]);
  return rows.length
    ? rows.map(([key, value]) => <div key={key}><Typography.Text type="secondary">{key}：</Typography.Text>
      <Typography.Text code ellipsis style={{ maxWidth: 520 }}>{value}</Typography.Text></div>)
    : null;
};

// caseChildren 只读展示单个用例的全部可审计配置：断言方式/启用标记、会话剧本
// 或单轮输入、期望输出、AI 判定配置、过程判定配置与生成来源。仅展示，编辑/删除
// 通过回调交给父层（本组件不发消息、不调 api）。
const caseChildren = (testCase: EvaluationCase, canManage: boolean,
  onEditCase?: (testCase: EvaluationCase) => void, onDeleteCase?: (testCase: EvaluationCase) => void) => <>
  <Flex gap={8} wrap style={{ marginBottom: 8 }}>
    {modeTag(testCase.assertion_mode)}
    {testCase.enabled ? <Tag color="green">包含在本版本</Tag> : <Tag>已从本版本排除</Tag>}
  </Flex>
  {testCase.session ? <div><Typography.Text type="secondary">会话剧本</Typography.Text><br />
    <Typography.Text>Goal：{testCase.session.goal}</Typography.Text>
    {testCase.session.turns.map((turn, index) => <div key={index} style={{ marginTop: 8 }}>
      <Typography.Text type="secondary">第 {index + 1} 轮用户消息</Typography.Text><br />
      <SafeValue value={turn.user} />
      {turn.probe && <div style={{ marginTop: 4 }}><Typography.Text type="secondary">探针期望：</Typography.Text>
        <SafeValue value={turn.probe} /></div>}
      {turn.tool_spec && <div style={{ marginTop: 4 }}>
        <Typography.Text type="secondary">本轮工具断言：</Typography.Text>
        <Typography.Text>{toolSpecSummary(turn.tool_spec)}</Typography.Text>
      </div>}
    </div>)}
  </div> : <div><Typography.Text type="secondary">测试输入</Typography.Text><br /><SafeValue value={testCase.input} /></div>}
  <div style={{ marginTop: 8 }}><Typography.Text type="secondary">期望输出</Typography.Text><br /><SafeValue value={testCase.expected_output} /></div>
  {testCase.assertion_mode === 'judge' && testCase.judge_spec && <div style={{ marginTop: 8 }}>
    <Typography.Text type="secondary">AI 判定配置</Typography.Text><br />
    <Typography.Text>模型：{testCase.judge_spec.model || '—'}</Typography.Text><br />
    <Typography.Text>评分标准：{testCase.judge_spec.rubric || '—'}</Typography.Text>
  </div>}
  {(testCase.tool_spec || testCase.step_judge?.criteria) && <div style={{ marginTop: 8 }}>
    <Typography.Text type="secondary">过程判定配置</Typography.Text><br />
    {testCase.tool_spec && <Typography.Text>工具断言：{toolSpecSummary(testCase.tool_spec)}</Typography.Text>}
    {testCase.step_judge?.criteria && <>
      {testCase.tool_spec && <br />}
      <Typography.Text>步骤判定：{testCase.step_judge.criteria}</Typography.Text>
    </>}
  </div>}
  {provenanceOf(testCase) && <div style={{ marginTop: 8 }}>
    <Typography.Text type="secondary">生成来源</Typography.Text><br />{provenanceOf(testCase)}
  </div>}
  {(canManage && (onEditCase || onDeleteCase)) && <Space style={{ marginTop: 12 }} wrap>
    {onEditCase && <Button type="link" size="small" onClick={() => onEditCase(testCase)}>编辑</Button>}
    {onDeleteCase && <Button type="link" size="small" danger onClick={() => onDeleteCase(testCase)}>删除</Button>}
  </Space>}
</>;

// SuiteCaseCollapse 是评测集 case 的只读折叠列表（草稿编辑与版本查看共用）。
// 展示型组件：不调 api、不发消息，编辑/删除仅回调父层（父层负责 confirm 与
// 消息）。defaultActiveKey 全展开；cases 列表切换时请用外层 key 重挂载以复位展开态。
export const SuiteCaseCollapse = ({ cases, canManage = false, onEditCase, onDeleteCase, emptyText }: {
  cases: EvaluationCase[]; canManage?: boolean;
  onEditCase?: (testCase: EvaluationCase) => void; onDeleteCase?: (testCase: EvaluationCase) => void;
  emptyText?: string;
}) => {
  if (cases.length === 0) return emptyText ? <Alert type="info" showIcon message={emptyText} /> : null;
  return <Collapse defaultActiveKey={cases.map((testCase) => testCase.id || testCase.name)}
    items={cases.map((testCase) => ({
      key: testCase.id || testCase.name,
      label: <Flex gap={8} align="center">{testCase.name || '未命名'}{modeTag(testCase.assertion_mode)}</Flex>,
      children: caseChildren(testCase, canManage, onEditCase, onDeleteCase),
    }))} />;
};
