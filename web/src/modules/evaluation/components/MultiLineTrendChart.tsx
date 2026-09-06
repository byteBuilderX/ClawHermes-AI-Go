// MultiLineTrendChart 渲染按日桶的多序列折线（native SVG，spec 2026-09-03 §5.2）。
// 单图只承载同一 y 量纲的序列：percent（[0,1] 通过率，轴固定）或 number（同单位计数，
// 轴按数据自动取整）。某桶某序列缺值（null）时该序列断开，不跨空值连线——与
// HealthTrendChart 的 lineSegments 语义一致。多序列以颜色区分并配图例，无 hover 重型图表。

export interface TrendBucket {
  /** x 轴刻度短标签（MM-DD）。 */
  bucketLabel: string;
  /** RFC3339，用于 <title> 完整时间。 */
  fullLabel: string;
}

export interface MultiLineSeries {
  name: string;
  color: string;
  /** 与 buckets 等长；null 表示该桶缺该序列（断线）。 */
  values: (number | null)[];
}

export interface MultiLineTrendChartProps {
  buckets: TrendBucket[];
  series: MultiLineSeries[];
  /** percent：数据为 [0,1] 通过率，y 固定 0..1；number：数据为同单位原始值，y 自动取整。 */
  unit: 'percent' | 'number';
  ariaLabel: string;
  /** 轴刻度文本格式化（接收「刻度对应的数据值」：percent 为 0..1 比例，number 为绝对数）。 */
  yTickLabel: (value: number) => string;
  /** 全序列无任何有效值时展示的空态文案（诚实表达，不画 0 轴假图）。 */
  noDataText: string;
  dataTestId: string;
}

const WIDTH = 640;
const HEIGHT = 220;
const MARGIN = { top: 14, right: 16, bottom: 30, left: 46 };
const PLOT_WIDTH = WIDTH - MARGIN.left - MARGIN.right;
const PLOT_HEIGHT = HEIGHT - MARGIN.top - MARGIN.bottom;
const MAX_TICK_LABELS = 6;
const GRID_RATIOS = [0, 0.25, 0.5, 0.75, 1];
const gridColor = '#e5e5e5';
const textColor = '#6b6b6b';

const xOf = (i: number, n: number) => (n === 1 ? MARGIN.left + PLOT_WIDTH / 2 : MARGIN.left + (i / (n - 1)) * PLOT_WIDTH);
const yOf = (ratio: number) => MARGIN.top + (1 - ratio) * PLOT_HEIGHT;

function tickSubset(n: number): number[] {
  if (n <= MAX_TICK_LABELS) {
    return Array.from({ length: n }, (_, i) => i);
  }
  const step = (n - 1) / (MAX_TICK_LABELS - 1);
  return Array.from({ length: MAX_TICK_LABELS }, (_, i) => Math.round(i * step));
}

// segmentPath 把一条序列的值映射为折线 path：null 处断开。
function segmentPath(values: (number | null)[], scale: number): string | null {
  const parts: string[] = [];
  let pen: string | null = null;
  for (let i = 0; i < values.length; i += 1) {
    const value = values[i];
    if (value === null) { pen = null; continue; }
    const ratio = Math.max(0, Math.min(1, value / scale));
    const command = `${xOf(i, values.length).toFixed(1)} ${yOf(ratio).toFixed(1)}`;
    parts.push(pen === null ? `M${command}` : `${pen}L${command}`);
    pen = '';
  }
  return parts.length ? parts.join(' ') : null;
}

export const MultiLineTrendChart = ({ buckets, series, unit, ariaLabel, yTickLabel, noDataText, dataTestId }: MultiLineTrendChartProps) => {
  const n = buckets.length;
  const finiteValues = series.flatMap((s) => s.values).filter((v): v is number => v !== null);
  // percent 轴固定 [0,1]；number 轴取全序列有限最大值（0 兜底避免除零）。
  const axisMax = unit === 'percent' ? 1 : Math.max(1, ...finiteValues);
  const hasData = finiteValues.length > 0;
  const ticks = tickSubset(n);

  if (!hasData) {
    return <div data-testid={dataTestId}>{noDataText}</div>;
  }

  return (
    <div data-testid={dataTestId}>
      <svg width={WIDTH} height={HEIGHT} viewBox={`0 0 ${WIDTH} ${HEIGHT}`} role="img" aria-label={ariaLabel}
        style={{ width: '100%', height: 'auto', display: 'block' }}>
        {GRID_RATIOS.map((ratio) => (
          <g key={ratio}>
            <line x1={MARGIN.left} x2={WIDTH - MARGIN.right} y1={yOf(ratio)} y2={yOf(ratio)} stroke={gridColor} strokeWidth={1} />
            <text x={MARGIN.left - 6} y={yOf(ratio) + 4} textAnchor="end" fontSize={11} fill={textColor}>
              {yTickLabel(axisMax * ratio)}
            </text>
          </g>
        ))}
        {series.map((s) => {
          const path = segmentPath(s.values, axisMax);
          return path
            ? <path key={s.name} d={path} fill="none" stroke={s.color} strokeWidth={2} strokeLinejoin="round" />
            : null;
        })}
        {buckets.map((bucket, i) => (
          <g key={bucket.fullLabel}>
            {series.map((s) => {
              const value = s.values[i];
              if (value === null) return null;
              const ratio = Math.max(0, Math.min(1, value / axisMax));
              return <circle key={s.name} cx={xOf(i, n)} cy={yOf(ratio)} r={3} fill={s.color} stroke="#fff" strokeWidth={1} />;
            })}
          </g>
        ))}
        {ticks.map((i) => (
          <text key={`${buckets[i].fullLabel}-tick`} x={xOf(i, n)} y={HEIGHT - MARGIN.bottom + 16} textAnchor="middle"
            fontSize={10} fill={textColor}>{buckets[i].bucketLabel}</text>
        ))}
      </svg>
      <div style={{ marginTop: 6, fontSize: 12, color: textColor }}>
        {series.map((s) => (
          <span key={s.name} style={{ marginRight: 14 }}>
            <svg width={10} height={10} style={{ verticalAlign: -1, marginRight: 4 }}>
              <circle cx={5} cy={5} r={4} fill={s.color} />
            </svg>
            {s.name}
          </span>
        ))}
      </div>
    </div>
  );
};
