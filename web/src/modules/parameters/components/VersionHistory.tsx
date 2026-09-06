import {
  Button,
  Empty,
  Modal,
  Space,
  Table,
  Tag,
  Typography,
  message,
} from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { useCallback, useEffect, useMemo, useState } from 'react';
import type { ReactNode } from 'react';

import { parametersApi } from '../api/parameters.api';
import type {
  PlatformConfigVersion,
  PlatformValues,
} from '../model/parameters';

import { extractErrorMessage, isForbidden } from '@/shared/lib';
import { VersionDiffDrawer } from '@/shared/ui';

const { Text } = Typography;

// 版本状态 → 标签展示。draft 是唯一可编辑态，published 生效可回滚，
// archived 由保留上限自动修剪。
const STATUS_TAG: Record<string, { color: string; label: string }> = {
  draft: { color: 'orange', label: '草稿' },
  published: { color: 'green', label: '已发布' },
  archived: { color: 'default', label: '已归档' },
};

// 操作者展示：backfill 的 system 归因在审计视图里读起来是"系统初始化"。
const ACTOR_LABELS: Record<string, string> = { system: '系统' };

const actorLabel = (actor: string): string => ACTOR_LABELS[actor] ?? actor;

const VersionHistory = ({
  groupKey,
  labelMap,
  refreshTick,
  onEffectiveChange,
  disabled = false,
}: {
  groupKey: string;
  labelMap?: Record<string, string>;
  refreshTick?: number;
  onEffectiveChange?: (values: PlatformValues) => void;
  // 只读成员置灰发布/回滚；默认 false（既有调用方不受影响）。
  disabled?: boolean;
}) => {
  const [versions, setVersions] = useState<PlatformConfigVersion[]>([]);
  const [loading, setLoading] = useState(false);
  // detailVersion 为「详情」Drawer 当前展示的版本；null = 关闭。
  const [detailVersion, setDetailVersion] = useState<PlatformConfigVersion | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const rows = await parametersApi.versions(groupKey);
      setVersions(rows ?? []);
    } catch (err) {
      if (!isForbidden(err)) {
        message.error({ content: extractErrorMessage(err, '加载版本历史失败'), duration: 3 });
      }
    } finally {
      setLoading(false);
    }
  }, [groupKey]);

  useEffect(() => {
    void load();
  }, [load, refreshTick]);

  // 生效快照按 id 索引，供 base_version_id diff 链与"当前生效"判定使用。
  const byId = useMemo(() => {
    const m = new Map<number, PlatformConfigVersion>();
    for (const v of versions) m.set(v.id, v);
    return m;
  }, [versions]);

  // 「详情」Drawer 素材：before = base_version_id 所指版本快照（发布时生效的
  // production 版本）；首版/基线缺失时为空对象（全部视为新增），由共享 Drawer
  // 现算逐字段前后值，前端不跨组拼快照字符串比对。
  const detail = useMemo(() => {
    if (!detailVersion) return null;
    const base =
      detailVersion.base_version_id != null ? byId.get(detailVersion.base_version_id) : undefined;
    return {
      title: `版本 v${detailVersion.version_seq} 字段变更`,
      before: base?.snapshot ?? {},
      after: detailVersion.snapshot,
    };
  }, [detailVersion, byId]);

  // production 所指版本 = 当前生效（回滚无意义，禁按钮）。由服务端按
  // production label 推导 is_current：前端不跨组拼快照字符串比对——真实多
  // 分组下 PlatformValues 是平铺 map、快照是分组粒度，JSON 比对恒 false，
  // 会让 production 所指版本也露出「回滚」按钮。
  const isCurrent = useCallback(
    (v: PlatformConfigVersion): boolean => v.status === 'published' && v.is_current,
    [],
  );

  // 发布/回滚成功后通过 onEffectiveChange 回传生效快照，父级据此递增 refreshTick
  // 触发重拉——避免 act 内直接 load() 与 tick 驱动造成双重请求。
  const act = useCallback(
    async (fn: () => Promise<PlatformValues>, successMsg: string) => {
      try {
        const values = await fn();
        message.success({ content: successMsg, duration: 2 });
        onEffectiveChange?.(values);
      } catch (err) {
        if (!isForbidden(err)) {
          message.error({ content: extractErrorMessage(err, '操作失败'), duration: 3 });
        }
      }
    },
    [onEffectiveChange],
  );

  const columns = useMemo<ColumnsType<PlatformConfigVersion>>(() => {
    const currentLabel = (v: PlatformConfigVersion) =>
      isCurrent(v) ? (
        <Tag color="blue" style={{ marginInlineEnd: 4 }}>
          当前生效
        </Tag>
      ) : null;

    return [
      {
        title: '版本',
        dataIndex: 'version_seq',
        width: 80,
        render: (seq: number) => `v${seq}`,
      },
      {
        title: '状态',
        dataIndex: 'status',
        width: 110,
        render: (_: unknown, v: PlatformConfigVersion) => {
          const tag = STATUS_TAG[v.status];
          return (
            <>
              {currentLabel(v)}
              <Tag color={tag?.color}>{tag?.label ?? v.status}</Tag>
            </>
          );
        },
      },
      {
        title: '变更说明',
        dataIndex: 'message',
        ellipsis: true,
        render: (msg: string) => msg || <Text type="secondary">—</Text>,
      },
      {
        title: '操作者',
        dataIndex: 'created_by_name',
        width: 140,
        // 优先展示服务端 join 出的可读名；缺失（如老版本无该字段）回退原始 actor。
        // system/api 等字面 actor 无 users 命中，created_by_name 回退原文后仍走
        // actorLabel 映射（system → 系统）。
        render: (_: unknown, v: PlatformConfigVersion) =>
          actorLabel(v.created_by_name || v.created_by),
      },
      {
        title: '时间',
        dataIndex: 'created_at',
        width: 180,
        render: (t: string) => new Date(t).toLocaleString('zh-CN', { hour12: false }),
      },
      {
        title: '操作',
        key: 'actions',
        width: 160,
        render: (_: unknown, v: PlatformConfigVersion) => {
          const actions: ReactNode[] = [];
          if (v.status === 'draft') {
            actions.push(
              <Button
                key="publish"
                type="link"
                size="small"
                disabled={disabled}
                onClick={() => {
                  Modal.confirm({
                    title: `发布版本 v${v.version_seq}？`,
                    content: '发布后 production/latest 将指向该版本，参数立即生效。',
                    okText: '发布',
                    cancelText: '取消',
                    onOk: () =>
                      act(
                        () => parametersApi.publish(groupKey, v.id),
                        `版本 v${v.version_seq} 已发布`,
                      ),
                  });
                }}
              >
                发布
              </Button>,
            );
          }
          if (v.status === 'published' && !isCurrent(v)) {
            actions.push(
              <Button
                key="rollback"
                type="link"
                size="small"
                danger
                disabled={disabled}
                onClick={() => {
                  Modal.confirm({
                    title: `回滚到版本 v${v.version_seq}？`,
                    // 影响范围与既有平台的版本发布×租户资源变更责任边界一致：只影响
                    // 未显式声明该参数的租户资源（declared 优先），故明确写出来。
                    content:
                      '回滚后参数立即生效：所有未显式声明该参数的租户资源将回退到该版本的取值；' +
                      '不产生新版本，历史保留可再次回滚。',
                    okText: '回滚',
                    okButtonProps: { danger: true },
                    cancelText: '取消',
                    onOk: () =>
                      act(
                        () => parametersApi.rollback(groupKey, v.id),
                        `已回滚到版本 v${v.version_seq}`,
                      ),
                  });
                }}
              >
                回滚
              </Button>,
            );
          }
          actions.push(
            <Button key="detail" type="link" size="small" onClick={() => setDetailVersion(v)}>
              详情
            </Button>,
          );
          return <Space size={0}>{actions}</Space>;
        },
      },
    ];
  }, [groupKey, isCurrent, act, disabled]);

  return (
    <div style={{ marginTop: 24 }}>
      <Space direction="vertical" size={8} style={{ width: '100%' }}>
        <Typography.Text strong>版本历史（配置变更审计）</Typography.Text>
        <Text type="secondary" style={{ fontSize: 12 }}>
          每次保存产生一个版本；点击「详情」对比该版本与其发布时基线的逐字段变更，
          回滚将 production 指回历史版本并在审计表留痕。
        </Text>
        <Table<PlatformConfigVersion>
          rowKey="id"
          size="small"
          loading={loading}
          columns={columns}
          dataSource={versions}
          pagination={{ pageSize: 5, showSizeChanger: false }}
          locale={{
            emptyText: loading ? null : <Empty description="暂无版本记录" image={Empty.PRESENTED_IMAGE_SIMPLE} />,
          }}
        />
      </Space>
      <VersionDiffDrawer
        open={detail !== null}
        onClose={() => setDetailVersion(null)}
        title={detail?.title}
        fieldLabels={labelMap}
        before={detail?.before ?? {}}
        after={detail?.after ?? {}}
      />
    </div>
  );
};

export default VersionHistory;
