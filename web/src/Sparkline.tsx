interface Props {
  values: number[];
  height?: number;
  /** CSS color string. Defaults to the theme accent (var(--accent)). */
  color?: string;
}

export default function Sparkline({ values, height = 36, color = 'var(--accent)' }: Props) {
  const w = Math.max(values.length, 1);
  const max = Math.max(1, ...values);
  const points = values
    .map((v, i) => {
      const x = (i / (w - 1 || 1)) * 100;
      const y = height - (v / max) * (height - 2) - 1;
      return `${x.toFixed(2)},${y.toFixed(2)}`;
    })
    .join(' ');

  // Drive SVG paint via currentColor so a single CSS variable controls both
  // the stroke and the fill gradient.
  return (
    <svg
      viewBox={`0 0 100 ${height}`}
      preserveAspectRatio="none"
      className="w-full"
      height={height}
      style={{ color }}
    >
      <defs>
        <linearGradient id="sparkfill" x1="0" x2="0" y1="0" y2="1">
          <stop offset="0%" stopColor="currentColor" stopOpacity="0.35" />
          <stop offset="100%" stopColor="currentColor" stopOpacity="0" />
        </linearGradient>
      </defs>
      <polygon
        points={`0,${height} ${points} 100,${height}`}
        fill="url(#sparkfill)"
      />
      <polyline
        points={points}
        fill="none"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinejoin="round"
        strokeLinecap="round"
        vectorEffect="non-scaling-stroke"
      />
    </svg>
  );
}
