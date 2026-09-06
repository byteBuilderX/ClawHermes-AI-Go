import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { Form, type FormInstance } from 'antd';
import { describe, expect, it, vi } from 'vitest';

import type { ScheduledTask } from '../model/scheduledTask';

import { ScheduledTaskFormModal, type ScheduledTaskFormValues } from './ScheduledTaskFormModal';

import { workflowApi } from '@/modules/workflow/api/workflow.api';

vi.mock('@/modules/workflow/api/workflow.api', () => ({
  workflowApi: { listWorkflowVersions: vi.fn(), listWorkflows: vi.fn() },
}));


const editingTask: ScheduledTask = {
  id: 'task-1',
  name: 'nightly',
  workflowId: 'wf-1',
  versionId: 'ver-1',
  inputTemplate: { task: 'summarize', deep: { level: 2 } },
  cronExpr: '0 9 * * *',
  enabled: true,
  nextFireAt: '2026-08-09T13:00:00Z',
  lastRunStatus: 'ok',
  createdBy: 'admin-1',
  createdAt: '2026-08-09T11:00:00Z',
  updatedAt: '2026-08-09T11:00:00Z',
};

function renderModal({ editing, onSubmit }: { editing?: ScheduledTask | null; onSubmit?: (v: ScheduledTaskFormValues) => void }) {
  const harness = vi.fn<(form: FormInstance) => void>();
  const FormHarness = () => {
    const [form] = Form.useForm<ScheduledTaskFormValues>();
    harness(form);
    return (
      <ScheduledTaskFormModal
        open
        loading={false}
        form={form}
        editing={editing ?? null}
        onClose={vi.fn()}
        onSubmit={onSubmit ?? vi.fn()}
      />
    );
  };
  render(<FormHarness />);
  return harness;
}

describe('ScheduledTaskFormModal', () => {
  beforeEach(() => {
    vi.mocked(workflowApi.listWorkflows).mockResolvedValue({ workflows: [], total: 0, page: 1, page_size: 50 });
    vi.mocked(workflowApi.listWorkflowVersions).mockResolvedValue({
      versions: [{ id: 'ver-1', definition_id: 'wf-1', version: 2, name: '稳定版', description: '', created_by: '', created_by_name: '', created_at: '2026-07-24T00:00:00Z' }],
      total: 1, page: 1, page_size: 50,
    });
  });

  it('prefills the form with formatted JSON when editing', async () => {
    const getForm = renderModal({ editing: editingTask });
    await waitFor(() => {
      const form = getForm.mock.calls[0][0] as FormInstance;
      expect(form.getFieldValue('name')).toBe('nightly');
      expect(form.getFieldValue('workflowId')).toBe('wf-1');
      expect(form.getFieldValue('cronExpr')).toBe('0 9 * * *');
    });
    const form = getForm.mock.calls[0][0] as FormInstance;
    expect(form.getFieldValue('inputTemplate')).toBe(JSON.stringify(editingTask.inputTemplate, null, 2));
  });

  it('loads versions for the prefilled workflow and shows them in the dropdown when editing', async () => {
    renderModal({ editing: editingTask });
    // 编辑模式 setFieldsValue 预填 workflowId，不触发 Select onChange——版本列表应主动加载。
    await waitFor(() => expect(workflowApi.listWorkflowVersions).toHaveBeenCalledWith('wf-1', expect.anything()));
    await waitFor(() => expect(screen.getByText('v2 稳定版')).toBeInTheDocument());
  });

  it('defaults the input template to the task placeholder when creating', async () => {
    const getForm = renderModal({ editing: null });
    await waitFor(() => {
      const form = getForm.mock.calls[0][0] as FormInstance;
      expect(form.getFieldValue('inputTemplate')).toBe('{\n  "task": ""\n}');
      expect(form.getFieldValue('name')).toBeUndefined();
    });
  });

  it('blocks submit with an invalid JSON input template', async () => {
    const onSubmit = vi.fn();
    renderModal({ editing: null, onSubmit });
    const name = screen.getByLabelText('名称');
    fireEvent.change(name, { target: { value: 'nightly' } });
    const textarea = screen.getByLabelText('输入模板');
    fireEvent.change(textarea, { target: { value: '{not json' } });

    const formEl = document.querySelector('form');
    expect(formEl).not.toBeNull();
    fireEvent.submit(formEl!);
    // 非法 JSON 由 JSON.parse 的 SyntaxError 文案呈现，此处只断言错误提示出现且未提交。
    await waitFor(() => {
      expect(document.querySelector('.ant-form-item-explain-error')).not.toBeNull();
    });
    expect(onSubmit).not.toHaveBeenCalled();
  });

  it('submits parsed template values on valid input', async () => {
    const onSubmit = vi.fn();
    const getForm = renderModal({ editing: null, onSubmit });
    const name = screen.getByLabelText('名称');
    fireEvent.change(name, { target: { value: 'nightly' } });
    const textarea = screen.getByLabelText('输入模板');
    fireEvent.change(textarea, { target: { value: '{"task":"summarize"}' } });
    const cron = screen.getByLabelText('Cron 表达式');
    fireEvent.change(cron, { target: { value: '0 9 * * *' } });
    // 工作流/版本 Select 懒加载 options，直接经 form 实例补值绕过 UI。
    const form = getForm.mock.calls[0][0] as FormInstance;
    form.setFieldsValue({ workflowId: 'wf-1', versionId: 'ver-1' });

    const formEl = document.querySelector('form');
    fireEvent.submit(formEl!);
    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1));
    const values = onSubmit.mock.calls[0][0] as ScheduledTaskFormValues;
    expect(values.name).toBe('nightly');
    expect(values.inputTemplate).toBe('{"task":"summarize"}');
    expect(values.cronExpr).toBe('0 9 * * *');
  });
});
