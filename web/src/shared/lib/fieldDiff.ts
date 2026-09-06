// fieldDiff：递归比较两份版本内容快照，产出逐字段「变更前/变更后」叶子差异。
// 纯函数，输入为 JSON 反序列化后的对象（snapshot/payload），输出可稳定渲染。

/** 单条字段差异：path 为 JSONPath 形式（如 `spec.nodes[2].name`）。新增只带
 *  after、删除只带 before、修改两侧都带。 */
export interface FieldChange {
  path: string;
  before?: unknown;
  after?: unknown;
}

type JsonContainer = Record<string, unknown> | unknown[];

/** 最大递归深度：超过即把整棵子树作为单条差异输出，防止超深嵌套失控。 */
const MAX_DIFF_DEPTH = 32;

/** 是否为 JSON 普通对象（排除 null 与数组）。 */
const isPlainObject = (value: unknown): value is Record<string, unknown> =>
  typeof value === 'object' && value !== null && !Array.isArray(value);

/** 语义深比较：忽略对象键顺序。快照经序列化往返后键序可能变化，逐键比较避免伪差异。 */
const deepEqual = (a: unknown, b: unknown): boolean => {
  if (Object.is(a, b)) return true;
  if (Array.isArray(a) && Array.isArray(b)) {
    if (a.length !== b.length) return false;
    return a.every((item, i) => deepEqual(item, b[i]));
  }
  if (isPlainObject(a) && isPlainObject(b)) {
    const keys = Object.keys(a);
    if (keys.length !== Object.keys(b).length) return false;
    return keys.every((k) => Object.prototype.hasOwnProperty.call(b, k) && deepEqual(a[k], b[k]));
  }
  return false;
};

/** 两侧都是对象或都是数组时才继续下钻；容器形态不一致则整值输出。 */
const bothComposite = (a: unknown, b: unknown): boolean =>
  (isPlainObject(a) && isPlainObject(b)) || (Array.isArray(a) && Array.isArray(b));

/** 容器子键的稳定遍历顺序：数组按下标升序，对象按键字典序。 */
const childKeysOf = (container: JsonContainer): string[] => {
  if (Array.isArray(container)) return container.map((_, i) => String(i));
  return Object.keys(container).sort();
};

/** 读取容器在 key 处的存在性与值；数组按数字下标，对象按自有键。 */
const readAt = (container: JsonContainer, key: string): { has: boolean; value: unknown } => {
  if (Array.isArray(container)) {
    const index = Number(key);
    return { has: index < container.length, value: container[index] };
  }
  return { has: Object.prototype.hasOwnProperty.call(container, key), value: container[key] };
};

/** 只写入存在的侧：增删缺省侧不落字段，渲染侧据此显示占位。 */
const pushDiff = (out: FieldChange[], path: string, before?: unknown, after?: unknown): void => {
  const change: FieldChange = { path };
  if (before !== undefined) change.before = before;
  if (after !== undefined) change.after = after;
  out.push(change);
};

/** 递归主体：先判等、再判深度/形态护栏，最后对子键做差集并下钻。 */
const walk = (out: FieldChange[], before: unknown, after: unknown, path: string, depth: number): void => {
  if (deepEqual(before, after)) return;
  if (depth >= MAX_DIFF_DEPTH || !bothComposite(before, after)) {
    pushDiff(out, path, before, after);
    return;
  }
  const b = before as JsonContainer;
  const c = after as JsonContainer;
  const arr = Array.isArray(b);
  const seen = new Set<string>();
  for (const key of [...childKeysOf(b), ...childKeysOf(c)]) seen.add(key);
  for (const key of seen) {
    const childPath = path ? (arr ? `${path}[${key}]` : `${path}.${key}`) : key;
    const bv = readAt(b, key);
    const cv = readAt(c, key);
    if (bv.has && cv.has) walk(out, bv.value, cv.value, childPath, depth + 1);
    else pushDiff(out, childPath, bv.has ? bv.value : undefined, cv.has ? cv.value : undefined);
  }
};

/** 比较两份对象快照，返回路径有序的字段差异列表（顺序稳定、可渲染）。 */
export const computeFieldChanges = (
  before: Record<string, unknown>,
  after: Record<string, unknown>,
): FieldChange[] => {
  const out: FieldChange[] = [];
  walk(out, before, after, '', 0);
  return out;
};
