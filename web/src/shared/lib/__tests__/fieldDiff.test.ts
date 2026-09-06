import { describe, expect, it } from 'vitest';

import { computeFieldChanges } from '../fieldDiff';

describe('computeFieldChanges', () => {
  it('returns empty when snapshots are deep-equal (even with key order shuffled)', () => {
    expect(computeFieldChanges({ x: 1, y: 2, a: { m: 1, n: 2 } }, { y: 2, x: 1, a: { n: 2, m: 1 } })).toEqual([]);
    expect(computeFieldChanges({}, {})).toEqual([]);
  });

  it('reports scalar change with before/after at dotted path', () => {
    expect(computeFieldChanges({ name: 'old', keep: 1 }, { name: 'new', keep: 1 })).toEqual([
      { path: 'name', before: 'old', after: 'new' },
    ]);
  });

  it('reports nested object leaf change', () => {
    expect(computeFieldChanges({ spec: { model: 'qwen' } }, { spec: { model: 'deepseek' } })).toEqual([
      { path: 'spec.model', before: 'qwen', after: 'deepseek' },
    ]);
  });

  it('reports key added on one side as after-only, deleted as before-only', () => {
    expect(computeFieldChanges({ a: 1 }, { a: 1, b: { k: 'v' } })).toEqual([
      { path: 'b', after: { k: 'v' } },
    ]);
    expect(computeFieldChanges({ a: 1, b: 2 }, { a: 1 })).toEqual([
      { path: 'b', before: 2 },
    ]);
  });

  it('reports array element change by index path', () => {
    const before = { nodes: [{ name: 'a' }, { name: 'b' }] };
    const after = { nodes: [{ name: 'a' }, { name: 'B' }] };
    expect(computeFieldChanges(before, after)).toEqual([
      { path: 'nodes[1].name', before: 'b', after: 'B' },
    ]);
  });

  it('reports appended and removed array indices', () => {
    expect(computeFieldChanges({ nodes: [0, 1] }, { nodes: [0, 1, 2] })).toEqual([
      { path: 'nodes[2]', after: 2 },
    ]);
    // 位置语义：删除中间元素后，后续元素后移 → 该下标报值变更，越界报删除。
    expect(computeFieldChanges({ nodes: [0, 1, 2] }, { nodes: [0, 2] })).toEqual([
      { path: 'nodes[1]', before: 1, after: 2 },
      { path: 'nodes[2]', before: 2 },
    ]);
  });

  it('emits whole value when container shapes mismatch (object vs array / vs scalar)', () => {
    expect(computeFieldChanges({ a: { x: 1 } }, { a: [] })).toEqual([
      { path: 'a', before: { x: 1 }, after: [] },
    ]);
    expect(computeFieldChanges({ a: { x: 1 } }, { a: 'text' })).toEqual([
      { path: 'a', before: { x: 1 }, after: 'text' },
    ]);
  });

  it('treats semantically identical arrays (different references) as unchanged', () => {
    expect(computeFieldChanges({ list: ['x', { n: 1 }] }, { list: ['x', { n: 1 }] })).toEqual([]);
  });

  it('guards against runaway depth by collapsing to a whole-value diff', () => {
    const build = (leaf: string): Record<string, unknown> => {
      let node: unknown = { leaf };
      for (let i = 0; i < 40; i += 1) node = { next: node };
      return node as Record<string, unknown>;
    };
    const changes = computeFieldChanges(build('same'), build('changed'));
    expect(changes).toHaveLength(1);
    // 下钻到 MAX_DIFF_DEPTH(32) 层仍不等 → 在 32 层整值输出。
    expect(changes[0].path.split('.').length).toBe(32);
    expect(changes[0].after).toBeDefined();
    expect(changes[0].before).toBeDefined();
  });

  it('skips equal subtrees inside a larger change', () => {
    const before = { spec: { keep: { deep: [1, 2, 3] }, change: 'a' } };
    const after = { spec: { keep: { deep: [1, 2, 3] }, change: 'b' } };
    expect(computeFieldChanges(before, after)).toEqual([
      { path: 'spec.change', before: 'a', after: 'b' },
    ]);
  });
});
