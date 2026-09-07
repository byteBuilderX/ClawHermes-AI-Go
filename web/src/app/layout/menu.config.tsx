import {
  AppstoreOutlined,
  PlusCircleOutlined,
  DashboardOutlined,
  RobotOutlined,
  CommentOutlined,
  TeamOutlined,
  SettingOutlined,
  GlobalOutlined,
  ApiOutlined,
  BookOutlined,
  ThunderboltOutlined,
  ExperimentOutlined,
  BranchesOutlined,
  HistoryOutlined,
  ScheduleOutlined,
  AuditOutlined,
  DatabaseOutlined,
  SafetyCertificateOutlined,
} from '@ant-design/icons';
import type { MenuProps } from 'antd';

import type { User } from '@/modules/iam';

type MenuItem = NonNullable<MenuProps['items']>[number];

/**
 * label 一律用字符串,不用 <Link> ReactNode。
 * 根因(实测):antd Menu 每次路由切换对 26 个 <Link> 全量 reconcile,
 * 主线程阻塞 50-80ms,合成器保留旧帧产生残影/慢。
 * 字符串 label 后 reconcile 降至 ~0ms;导航由 key + AppShell 的 onClick 承担。
 */
export const buildMenuItems = (user: User | null | undefined): MenuItem[] => {
  const tenantRole = user?.role ?? user?.current_tenant?.role ?? 'member';
  const canManageTenant = tenantRole === 'admin' || tenantRole === 'owner';
  const base: MenuItem[] = [
    { key: '/', icon: <DashboardOutlined />, label: '概览' },
    { key: '/chat', icon: <CommentOutlined />, label: 'Agent 对话' },
    {
      key: 'workflow-group',
      icon: <BranchesOutlined />,
      label: '流程',
      children: [
        { key: '/workflows', icon: <BranchesOutlined />, label: '工作流' },
        { key: '/workflow-runs', icon: <HistoryOutlined />, label: '运行中心' },
        { key: '/scheduled-tasks', icon: <ScheduleOutlined />, label: '定时任务' },
        canManageTenant ? {
          key: '/workflows/new', icon: <PlusCircleOutlined />, label: '新建工作流',
        } : null,
      ],
    },
    {
      key: 'agent-group',
      icon: <RobotOutlined />,
      label: 'Agent',
      children: [
        { key: '/agents', icon: <RobotOutlined />, label: 'Agent 管理' },
        canManageTenant ? {
          key: '/agents/create',
          icon: <PlusCircleOutlined />,
          label: '创建 Agent',
        } : null,
      ],
    },
    {
      key: 'skill-group',
      icon: <ThunderboltOutlined />,
      label: '技能',
      children: [
        { key: '/skills', icon: <AppstoreOutlined />, label: '技能列表' },
        canManageTenant ? {
          key: '/skills/create',
          icon: <PlusCircleOutlined />,
          label: '创建技能',
        } : null,
      ],
    },
    {
      key: 'evaluation-group',
      icon: <ExperimentOutlined />,
      label: '评测',
      children: [
        { key: '/evaluations', icon: <ExperimentOutlined />, label: '评测与进化' },
        { key: '/evaluations/runs', icon: <HistoryOutlined />, label: '离线运行' },
        { key: '/evaluations/evolution', icon: <BranchesOutlined />, label: '自进化工作区' },
        { key: '/evaluations/resources', icon: <AppstoreOutlined />, label: '被测资源' },
        { key: '/evaluations/observability', icon: <ThunderboltOutlined />, label: '在线观测' },
        { key: '/evaluations/review', icon: <AuditOutlined />, label: '人工评审' },
        { key: '/evaluations/suites', icon: <DatabaseOutlined />, label: '评测集' },
      ],
    },
    {
      key: '/knowledge',
      icon: <BookOutlined />,
      label: '知识库',
    },
    {
      key: '/memory',
      icon: <DatabaseOutlined />,
      label: '我的记忆',
    },
    // 审批中心对 member 开放（发起人查看自己发起的审批），故常显；操作能力按角色
    // 在页面内区分（admin/owner 批准/拒绝/指派，member 只读）。
    {
      key: '/approvals',
      icon: <SafetyCertificateOutlined />,
      label: '审批中心',
    },
    {
      key: 'mcp-group',
      icon: <ApiOutlined />,
      label: 'MCP 服务器',
      children: [
        { key: '/mcp', icon: <ApiOutlined />, label: '服务器列表' },
        canManageTenant ? {
          key: '/mcp/create',
          icon: <PlusCircleOutlined />,
          label: '添加服务器',
        } : null,
      ],
    },
  ];

  if (user?.current_tenant) {
    base.push({
      key: 'tenant-group',
      icon: <TeamOutlined />,
      label: '团队',
      children: [
        {
          key: '/tenant/members',
          icon: <TeamOutlined />,
          label: '成员管理',
        },
        {
          key: '/tenant/settings',
          icon: <SettingOutlined />,
          label: '租户设置',
        },
      ],
    });
  }

  // 审计日志是租户级资源，租户 admin/owner 可见（owner 经 TENANT_ROLE_RANK
  // 自动通过）；global_admin 若无租户 admin 角色则不可见。
  if (user?.current_tenant && canManageTenant) {
    base.push({
      key: '/audit',
      icon: <AuditOutlined />,
      label: '审计日志',
    });
  }

  // 平台管理菜单对普通用户常显（需求：侧边栏可见，点击由 PrivateRoute 渲染 403）。
  // 各项操作权限仍由路由守卫 + 后端中间件双拦截：租户管理/平台参数/审计日志/模型管理要求
  // system_admin，平台管理员要求 global_admin（URL 直达同样被拦）。
  // /prompts、/mechanism 随 main 移除提示词管理（#374）与机制基线存储化一并删除。
  base.push({
    key: 'platform-admin-group',
    icon: <SettingOutlined />,
    label: '平台管理',
    children: [
      { key: '/models', icon: <ApiOutlined />, label: '模型管理' },
      { key: '/admin/tenants', icon: <GlobalOutlined />, label: '全局租户' },
      { key: '/admin/settings', icon: <SettingOutlined />, label: '平台参数' },
      { key: '/admin/admins', icon: <SafetyCertificateOutlined />, label: '平台管理员' },
      { key: '/admin/audit', icon: <AuditOutlined />, label: '审计日志' },
    ],
  });

  return base;
};

export const resolveOpenKeys = (pathname: string): string[] => {
  if (pathname.startsWith('/agents')) return ['agent-group'];
  if (pathname.startsWith('/skills')) return ['skill-group'];
  if (pathname.startsWith('/mcp')) return ['mcp-group'];
  if (pathname.startsWith('/evaluations')) return ['evaluation-group'];
  if (pathname.startsWith('/workflows') || pathname.startsWith('/workflow-runs') || pathname.startsWith('/scheduled-tasks')) return ['workflow-group'];
  if (pathname.startsWith('/tenant')) return ['tenant-group'];
  if (
    pathname.startsWith('/models') ||
    // /prompts、/mechanism 路由已随 main 删除（提示词管理 #374、机制基线存储化撤销）
    // /audit 是租户级顶层菜单项，不归入平台管理分组（openKeys 由顶层菜单直接管理）
    pathname.startsWith('/admin')
  )
    return ['platform-admin-group'];
  return [];
};

// 详情类子路由（/evaluations/runs/:id、/evaluations/resources/:kind/:id、套件详情）没有
// 独立菜单项，选中态归一为其所属的列表叶子菜单，避免深链后评测分组内无高亮。
const EVALUATION_DETAIL_ROOTS = ['/evaluations/runs', '/evaluations/resources', '/evaluations/suites'];
export const resolveSelectedKey = (pathname: string): string => {
  if (pathname.startsWith('/evaluations/')) {
    const root = EVALUATION_DETAIL_ROOTS.find((route) => pathname === route || pathname.startsWith(`${route}/`));
    if (root) return root;
  }
  return pathname;
};
