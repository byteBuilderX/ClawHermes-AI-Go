import { Button, Drawer, Empty, Form, Input, Modal, Select, Space, Table, Tag, message } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { useCallback, useEffect, useState } from 'react';

import { decideReviewItem, deleteReviewItem, listReviewItems } from '../services/review';
import type { ReviewItem, ReviewItemDecisionRequest } from '../types/review';

import { extractErrorMessage } from '@/shared/lib';

const VERDICT_OPTIONS = [
  { value: 'pass', label: '通过' },
  { value: 'fail', label: '不通过' },
  { value: 'judge_misjudgment', label: 'judge 误判' },
  { value: 'case_revision', label: '用例需修正' },
];

const REASON_LABELS: Record<string, string> = {
  low_confidence: '低置信度',
  dimension_split: '维度分歧',
  judge_rule_conflict: '规则与 judge 冲突',
  needs_review: '需人工复核',
  process_output_conflict: '输出通过但过程未通过',
};

const RISK_LABELS: Record<string, string> = { high: '高', medium: '中', low: '低' };
const RISK_COLORS: Record<string, string> = { high: 'red', medium: 'orange', low: 'blue' };

const VERDICT_LABELS: Record<string, string> = {
  pass: '通过',
  fail: '不通过',
  judge_misjudgment: 'judge 误判',
  case_revision: '用例需修正',
};

// reviewResourceText 评审条目的「资源」展示：有 resource_name 用真名（id 弱化随括），
// 否则保留原 kind:id 组合；两者皆空返回「-」（评审条目 case_result 来源的
// resource_kind/resource_id 恒为空的已知限制，显示占位符而非空白）。
const reviewResourceText = (item: ReviewItem): string => {
  const name = item.resource_name?.trim();
  if (name) return item.resource_id && item.resource_id !== name ? `${name}（${item.resource_id}）` : name;
  return item.resource_kind ? `${item.resource_kind}:${item.resource_id || '-'}` : '-';
};

export default function ReviewPoolPanel({ canDelete }: {
  // 删除可见性（RBAC）：owner 恒可删 / created_by 等于当前用户可删；由父级基于
  // useEvaluationCenter.canDeleteEntity 传入，panel 内只做确认 + 删除 + 刷新。
  canDelete: (item: ReviewItem) => boolean;
}) {
  const [items, setItems] = useState<ReviewItem[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(false);
  const [detail, setDetail] = useState<ReviewItem | null>(null);
  const [decisionTarget, setDecisionTarget] = useState<ReviewItem | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [form] = Form.useForm<ReviewItemDecisionRequest>();

  const load = useCallback(async (page = 1, pageSize = 10) => {
    setLoading(true);
    try {
      const data = await listReviewItems({ page, page_size: pageSize });
      setItems(data.items ?? []);
      setTotal(data.total ?? 0);
    } catch (err: any) {
      message.error({ content: extractErrorMessage(err, '加载评审池失败'), duration: 3 });
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const closeDecision = () => {
    form.resetFields();
    setDecisionTarget(null);
  };

  const confirmDelete = (item: ReviewItem) => {
    Modal.confirm({
      title: '删除该评审项？',
      content: '删除后无法恢复，关联的校准/归因记录将一并删除。',
      okText: '删除',
      okButtonProps: { danger: true },
      cancelText: '取消',
      onOk: async () => {
        try {
          await deleteReviewItem(item.id);
          message.success({ content: '评审项已删除', duration: 2 });
          load();
        } catch (err: any) {
          message.error({ content: extractErrorMessage(err, '删除失败'), duration: 3 });
          throw err;
        }
      },
    });
  };

  const submitDecision = async () => {
    if (!decisionTarget) return;
    let values: ReviewItemDecisionRequest;
    try {
      values = await form.validateFields();
    } catch {
      // 校验失败：antd 已在表单内展示错误，保持 Modal 打开
      return;
    }
    setSubmitting(true);
    try {
      await decideReviewItem(decisionTarget.id, values);
      message.success({ content: '评审已提交', duration: 2 });
      closeDecision();
      load();
    } catch (err: any) {
      message.error({ content: extractErrorMessage(err, '提交失败'), duration: 3 });
    } finally {
      setSubmitting(false);
    }
  };

  const columns: ColumnsType<ReviewItem> = [
    { title: '来源', dataIndex: 'source_type', width: 100,
      render: (v: string) => (v === 'observation' ? '观测' : '评测集') },
    { title: '原因', dataIndex: 'trigger_reason', width: 120,
      render: (v: string) => <Tag>{REASON_LABELS[v] ?? v}</Tag> },
    // case_result 来源条目的 resource_kind/resource_id 恒为空（Task 10 已知限制），显示占位符而非空白。
    { title: '资源', dataIndex: 'resource_kind', width: 180, render: (_, row) => reviewResourceText(row) },
    { title: '优先级', dataIndex: 'risk_level', width: 90,
      render: (v: string) => (v ? <Tag color={RISK_COLORS[v]}>{RISK_LABELS[v] ?? v}</Tag> : '-') },
    { title: '状态', dataIndex: 'status', width: 80,
      render: (v: string) => (v === 'pending' ? '待评审' : '已评审') },
    { title: '创建时间', dataIndex: 'created_at', width: 180 },
    {
      title: '操作',
      key: 'actions',
      width: 160,
      render: (_, record) => (
        <Space>
          <Button size="small" onClick={() => setDetail(record)}>详情</Button>
          {record.status === 'pending' && (
            <Button size="small" type="primary" onClick={() => setDecisionTarget(record)}>评审</Button>
          )}
          {canDelete(record) && <Button size="small" danger onClick={() => confirmDelete(record)}>删除</Button>}
        </Space>
      ),
    },
  ];

  return (
    <div>
      <Table<ReviewItem>
        rowKey="id"
        loading={loading}
        columns={columns}
        dataSource={items}
        pagination={{ total, pageSize: 10, onChange: (page) => load(page) }}
      />
      <Drawer title="评审详情" open={!!detail} onClose={() => setDetail(null)} width={520}>
        {detail ? (
          <>
            <Space direction="vertical" size={4} style={{ width: '100%', marginBottom: 12 }}>
              <div>来源：{detail.source_type === 'observation' ? '观测' : '评测集'} · 原因：{REASON_LABELS[detail.trigger_reason] ?? detail.trigger_reason} · 优先级：{detail.risk_level ? RISK_LABELS[detail.risk_level] : '-'}</div>
              <div>资源：{reviewResourceText(detail)} · 状态：{detail.status === 'pending' ? '待评审' : '已评审'}</div>
              {detail.human_verdict && <div>评审结论：{VERDICT_LABELS[detail.human_verdict] ?? detail.human_verdict}</div>}
              {detail.reviewer && <div>评审人：{detail.reviewer}</div>}
              {detail.review_reason && <div>评审理由：{detail.review_reason}</div>}
            </Space>
            <pre style={{ whiteSpace: 'pre-wrap' }}>{JSON.stringify(detail.snapshot, null, 2)}</pre>
          </>
        ) : <Empty />}
      </Drawer>
      <Modal
        title="人工评审"
        open={!!decisionTarget}
        onCancel={closeDecision}
        onOk={() => void submitDecision()}
        okText="提交评审"
        cancelText="取消"
        confirmLoading={submitting}
        destroyOnHidden
      >
        <Form form={form} layout="vertical">
          <Form.Item name="verdict" label="评审结论" rules={[{ required: true, message: '请选择评审结论' }]}>
            <Select aria-label="评审结论" options={VERDICT_OPTIONS} placeholder="请选择评审结论" />
          </Form.Item>
          <Form.Item name="reason" label="评审理由" rules={[{ required: true, message: '请填写评审理由' }]}>
            <Input.TextArea aria-label="评审理由" rows={4} maxLength={2048} showCount placeholder="请填写评审理由" />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  );
}
