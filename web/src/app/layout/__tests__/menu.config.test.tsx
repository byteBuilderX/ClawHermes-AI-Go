import { render, screen } from '@testing-library/react';
import type { ReactNode } from 'react';
import { describe, expect, it } from 'vitest';

import { buildMenuItems, resolveOpenKeys, resolveSelectedKey } from '../menu.config';

const collectLabels = (items: ReturnType<typeof buildMenuItems>): ReactNode[] =>
  items.flatMap((item) => {
    if (!item || typeof item !== 'object') return [];
    const current = 'label' in item ? [item.label] : [];
    const children = 'children' in item && Array.isArray(item.children)
      ? collectLabels(item.children)
      : [];
    return [...current, ...children];
  });

/**
 * label 是纯字符串(导航由 key + AppShell 的 onClick 承担,不再是 <Link>)。
 * 测试断言菜单项文本与权限过滤;href 语义由 E2E 覆盖。
 */
describe('buildMenuItems', () => {
  it('hides tenant management routes from members', () => {
    const labels = collectLabels(buildMenuItems({
          sub: 'user-1',
          tenant_id: 'tenant-1',
          role: 'member',
          avatar_url: '',
          github_login: 'member',
          username: '',
          current_tenant: { id: 'tenant-1', name: 'Test', role: 'member' },
        }));
    render(<div>{labels.map((label, index) => <div key={index}>{label}</div>)}</div>);

    expect(screen.getByText('Agent 管理')).toBeInTheDocument();
    expect(screen.getByText('技能列表')).toBeInTheDocument();
    expect(screen.getByText('服务器列表')).toBeInTheDocument();
    expect(screen.getByText('工作流')).toBeInTheDocument();
    expect(screen.queryByText('新建工作流')).not.toBeInTheDocument();
    expect(screen.queryByText('创建 Agent')).not.toBeInTheDocument();
    expect(screen.queryByText('创建技能')).not.toBeInTheDocument();
    expect(screen.queryByText('添加服务器')).not.toBeInTheDocument();
    // /evaluations hub（「评测与进化」）已拆除，评测组以离线运行叶子页为首项
    expect(screen.queryByText('评测与进化')).not.toBeInTheDocument();
    expect(screen.getByText('离线运行')).toBeInTheDocument();
  });

  it('opens the evaluation navigation group', () => {
    expect(resolveOpenKeys('/evaluations')).toEqual(['evaluation-group']);
  });

  it('shows workflow authoring only to tenant admins', () => {
    const labels = collectLabels(buildMenuItems({
      sub: 'admin-1', tenant_id: 'tenant-1', role: 'admin', avatar_url: '', github_login: 'admin', username: '',
      current_tenant: { id: 'tenant-1', name: 'Test', role: 'admin' },
    }));
    render(<div>{labels.map((label, index) => <div key={index}>{label}</div>)}</div>);
    expect(screen.getByText('工作流')).toBeInTheDocument();
    expect(screen.getByText('新建工作流')).toBeInTheDocument();
    expect(resolveOpenKeys('/workflows/new')).toEqual(['workflow-group']);
  });

  it('shows the platform admin group to tenant admins (read-only visible; controls role-gated)', () => {
    const adminLabels = collectLabels(buildMenuItems({
      sub: 'admin-1', tenant_id: 'tenant-1', role: 'admin', avatar_url: '', github_login: 'admin', username: '',
      current_tenant: { id: 'tenant-1', name: 'Test', role: 'admin' },
    }));
    render(<div>{adminLabels.map((label, index) => <div key={index}>{label}</div>)}</div>);
    // 平台管理菜单对普通用户常显；普通成员只读可见（编辑控件由 PlatformAdminGate 置灰）
    expect(screen.getByText('平台管理')).toBeInTheDocument();
    expect(screen.getByText('全局租户')).toBeInTheDocument();
    expect(screen.getByText('平台参数')).toBeInTheDocument();
    // 顶层租户 /audit + 平台管理组内 /admin/audit 各一个
    expect(screen.getAllByText('审计日志')).toHaveLength(2);
    // 审批中心对 member 开放(发起人查看自己发起的审批),租户 admin 常显
    expect(screen.getByText('审批中心')).toBeInTheDocument();
  });

  it('shows the platform admin group to members (read-only visible; controls role-gated)', () => {
    const memberLabels = collectLabels(buildMenuItems({
      sub: 'user-1', tenant_id: 'tenant-1', role: 'member', avatar_url: '', github_login: 'member', username: '',
      current_tenant: { id: 'tenant-1', name: 'Test', role: 'member' },
    }));
    render(<div>{memberLabels.map((label, index) => <div key={index}>{label}</div>)}</div>);
    // 平台管理组常显，member 只读（Gate 置灰），写权限按角色
    expect(screen.getByText('平台管理')).toBeInTheDocument();
    expect(screen.getByText('模型管理')).toBeInTheDocument();
    // member 无顶层 /audit（canManageTenant=false），仅平台管理组内一个
    expect(screen.getByText('审计日志')).toBeInTheDocument();
    expect(screen.getByText('全局租户')).toBeInTheDocument();
    expect(screen.getByText('平台参数')).toBeInTheDocument();
    // 审批中心对 member 常显:发起人需入口查看自己发起的审批(只读视角在页面内区分)
    expect(screen.getByText('审批中心')).toBeInTheDocument();
  });

  it('shows the full platform admin group to every role (route-level gating)', () => {
    const labels = collectLabels(buildMenuItems({
      sub: 'ga-1', tenant_id: 'tenant-1', role: 'member', global_role: 'global_admin',
      avatar_url: '', github_login: 'ga', username: '',
      current_tenant: { id: 'tenant-1', name: 'Test', role: 'member' },
    }));
    render(<div>{labels.map((label, index) => <div key={index}>{label}</div>)}</div>);
    expect(screen.getByText('平台管理')).toBeInTheDocument();
    expect(screen.getByText('模型管理')).toBeInTheDocument();
    // global_admin 租户角色 member：顶层 /audit 不可见，平台管理组内 /admin/audit 常显 → 1 个
    expect(screen.getAllByText('审计日志')).toHaveLength(1);
    expect(screen.getByText('全局租户')).toBeInTheDocument();
    expect(screen.getByText('平台参数')).toBeInTheDocument();
    expect(screen.getByText('平台管理员')).toBeInTheDocument();
  });

  it('shows tenant/settings and every platform admin item to system_admin', () => {
    const labels = collectLabels(buildMenuItems({
      sub: 'sa-1', tenant_id: 'tenant-1', role: 'member', global_role: 'system_admin',
      avatar_url: '', github_login: 'sa', username: '',
      current_tenant: { id: 'tenant-1', name: 'Test', role: 'member' },
    }));
    render(<div>{labels.map((label, index) => <div key={index}>{label}</div>)}</div>);
    expect(screen.getByText('平台管理')).toBeInTheDocument();
    expect(screen.getByText('全局租户')).toBeInTheDocument();
    expect(screen.getByText('平台参数')).toBeInTheDocument();
    // 菜单常显：模型管理与平台管理员同样展示（访问权限由路由守卫拦截）
    expect(screen.getByText('模型管理')).toBeInTheDocument();
    expect(screen.getByText('平台管理员')).toBeInTheDocument();
  });

  it('shows 审批中心 to tenant owners even when not global admin', () => {
    const labels = collectLabels(buildMenuItems({
      sub: 'owner-1', tenant_id: 'tenant-1', role: 'owner', avatar_url: '', github_login: 'owner', username: '',
      current_tenant: { id: 'tenant-1', name: 'Test', role: 'owner' },
    }));
    render(<div>{labels.map((label, index) => <div key={index}>{label}</div>)}</div>);
    expect(screen.getByText('审批中心')).toBeInTheDocument();
    expect(screen.getByText('平台管理')).toBeInTheDocument();
  });

  it('resolves platform admin paths to the merged open-key group', () => {
    expect(resolveOpenKeys('/models')).toEqual(['platform-admin-group']);
    expect(resolveOpenKeys('/admin/tenants')).toEqual(['platform-admin-group']);
    expect(resolveOpenKeys('/admin/settings')).toEqual(['platform-admin-group']);
    // 工具审批是独立菜单项,不再归入任何分组
    expect(resolveOpenKeys('/approvals')).toEqual([]);
    // /prompts、/mechanism 路由已随 main 删除,不归入任何分组
    expect(resolveOpenKeys('/prompts')).toEqual([]);
    expect(resolveOpenKeys('/mechanism/profiles')).toEqual([]);
  });

  it('shows the audit log to tenant owners as both top-level and platform-group items', () => {
    const labels = collectLabels(buildMenuItems({
      sub: 'owner-1', tenant_id: 'tenant-1', role: 'owner', avatar_url: '', github_login: 'owner', username: '',
      current_tenant: { id: 'tenant-1', name: 'Test', role: 'owner' },
    }));
    render(<div>{labels.map((label, index) => <div key={index}>{label}</div>)}</div>);
    // 顶层租户 /audit + 平台管理组内 /admin/audit 各一个
    expect(screen.getAllByText('审计日志')).toHaveLength(2);
    // /audit 是顶层菜单项,不归入任何分组
    expect(resolveOpenKeys('/audit')).toEqual([]);
  });

  it('keeps the platform-group audit log visible to a global admin without tenant admin role', () => {
    // global_admin 租户角色 member：顶层租户 /audit 不可见，但平台管理组内
    // /admin/audit 常显（访问权限由 PrivateRoute 403 承担）
    const labels = collectLabels(buildMenuItems({
      sub: 'ga-1', tenant_id: 'tenant-1', role: 'member', global_role: 'global_admin',
      avatar_url: '', github_login: 'ga', username: '',
    }));
    render(<div>{labels.map((label, index) => <div key={index}>{label}</div>)}</div>);
    expect(screen.getAllByText('审计日志')).toHaveLength(1);
  });

  it('does not expose execution history in navigation', () => {
    const items = buildMenuItems({
      sub: 'user-1',
      tenant_id: 'tenant-1',
      role: 'member',
      avatar_url: '',
      github_login: 'member',
      username: '',
      current_tenant: { id: 'tenant-1', name: 'Test', role: 'member' },
    });

    const labels = collectLabels(items);
    render(<div>{labels.map((label, index) => <div key={index}>{label}</div>)}</div>);

    expect(screen.queryByText('执行历史')).not.toBeInTheDocument();
    expect(items.some((item) => item && 'key' in item && item.key === '/history')).toBe(false);
    expect(resolveOpenKeys('/history')).toEqual([]);
    expect(resolveOpenKeys('/agents')).toEqual(['agent-group']);
  });

  it('lists the evaluation leaf pages as menu children for direct navigation', () => {
    const items = buildMenuItems({
      sub: 'admin-1', tenant_id: 'tenant-1', role: 'admin', avatar_url: '', github_login: 'admin', username: '',
      current_tenant: { id: 'tenant-1', name: 'Test', role: 'admin' },
    });
    const group = items.find((item) => item && 'key' in item && item.key === 'evaluation-group');
    const keys = (group && 'children' in group && Array.isArray(group.children)
      ? group.children.map((child) => (child && 'key' in child ? child.key : ''))
      : []).filter(Boolean);
    expect(keys).toEqual([
      '/evaluations/runs', '/evaluations/evolution', '/evaluations/resources',
      '/evaluations/observability', '/evaluations/review', '/evaluations/suites',
    ]);
  });

  describe('resolveSelectedKey', () => {
    it('keeps leaf routes untouched', () => {
      expect(resolveSelectedKey('/evaluations')).toBe('/evaluations');
      expect(resolveSelectedKey('/evaluations/evolution')).toBe('/evaluations/evolution');
    });

    it('collapses detail routes onto their owning list menu', () => {
      expect(resolveSelectedKey('/evaluations/runs/run-1')).toBe('/evaluations/runs');
      expect(resolveSelectedKey('/evaluations/resources/skill/skill-1')).toBe('/evaluations/resources');
      expect(resolveSelectedKey('/evaluations/suites/suite-1')).toBe('/evaluations/suites');
    });

    it('leaves other modules unchanged', () => {
      expect(resolveSelectedKey('/agents/agent-1/edit')).toBe('/agents/agent-1/edit');
    });
  });
});
