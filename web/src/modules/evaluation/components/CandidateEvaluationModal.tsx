import { Form, Modal } from 'antd';
import { useEffect, useRef, useState } from 'react';

import type { ResourceKind } from '../model/evaluation';

import { SuitePicker, type SuitePick } from './SuitePicker';

import { createIdempotencyKey } from '@/shared/lib/idempotencyKey';

export const CandidateEvaluationModal = ({ open, onClose, onSubmit, resourceKind }: {
  open: boolean; onClose: () => void;
  onSubmit: (suiteRevisionId: string, idempotencyKey: string) => Promise<void>;
  resourceKind: ResourceKind;
}) => {
  const [pick, setPick] = useState<SuitePick | null>(null);
  const [loading, setLoading] = useState(false);
  const idempotencyKey = useRef(createIdempotencyKey());
  const ready = Boolean(pick?.revisionId);
  // 每次打开重置选择与幂等键，避免重开残留上一次的评测套件选择。
  useEffect(() => {
    if (!open) return;
    setPick(null);
    idempotencyKey.current = createIdempotencyKey();
  }, [open]);
  const close = () => {
    idempotencyKey.current = createIdempotencyKey();
    setPick(null);
    onClose();
  };
  const submit = async () => {
    if (!pick?.revisionId) return;
    setLoading(true);
    try {
      await onSubmit(pick.revisionId, idempotencyKey.current);
      close();
    } catch {
      // The page owns the persistent error notification; keep the form open for correction or retry.
    } finally {
      setLoading(false);
    }
  };
  return <Modal title="运行候选离线评测" open={open} onCancel={close} onOk={() => void submit()}
    okText="开始评测" cancelText="取消" confirmLoading={loading} okButtonProps={{ disabled: !ready }} destroyOnHidden>
    <Form layout="vertical">
      <Form.Item label="评测套件" required extra="候选必须通过此评测套件后才能创建金丝雀实验。">
        <SuitePicker resourceKind={resourceKind} value={pick} onChange={setPick} />
      </Form.Item>
    </Form>
  </Modal>;
};
