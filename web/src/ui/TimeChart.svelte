<script>
  // Time series chart (1 or 2 series), following the dataviz rules: one
  // y-axis, 2px lines over a faint area, recessive grid, legend for 2
  // series + direct labels at the line ends, crosshair + tooltip on hover.
  import { t } from '../i18n.svelte.js'
  //   points: [{t (unix s), [key]: number}]   series: [{key, label, color}]
  let { points = [], series = [], height = 180, format = (v) => String(v), compact = false, timeFormat } = $props()

  let width = $state(600)
  let hover = $state(null) // index under the pointer

  const pad = $derived(compact ? { l: 0, r: 0, t: 6, b: 2 } : { l: 0, r: 64, t: 10, b: 22 })
  const innerW = $derived(Math.max(10, width - pad.l - pad.r))
  const innerH = $derived(height - pad.t - pad.b)
  const max = $derived(Math.max(1e-9, ...points.flatMap((p) => series.map((s) => p[s.key] ?? 0))) * 1.12)
  const x = (i) => pad.l + (points.length < 2 ? innerW : (i * innerW) / (points.length - 1))
  const y = (v) => pad.t + innerH - (Math.max(0, v ?? 0) / max) * innerH

  function line(key) {
    return points.map((p, i) => `${i ? 'L' : 'M'}${x(i).toFixed(1)},${y(p[key]).toFixed(1)}`).join('')
  }
  function area(key) {
    if (!points.length) return ''
    return `${line(key)}L${x(points.length - 1).toFixed(1)},${pad.t + innerH}L${x(0).toFixed(1)},${pad.t + innerH}Z`
  }

  const fmtTime = (t) =>
    timeFormat ? timeFormat(t) : new Date(t * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  // At most 5 time labels, never two on the same point (few points at first).
  const ticks = $derived(points.length > 1
    ? [...new Set([0, 0.25, 0.5, 0.75, 1].map((f) => Math.round(f * (points.length - 1))))].filter((i, n, a) => n === 0 || x(i) - x(a[n - 1]) > 70)
    : [])
  // End labels: spread apart when the lines end close together.
  const endY = $derived.by(() => {
    if (!points.length) return {}
    const lastP = points[points.length - 1]
    const ys = series.map((s) => ({ key: s.key, y: y(lastP[s.key]) + 4 })).sort((a, b) => a.y - b.y)
    for (let i = 1; i < ys.length; i++) if (ys[i].y - ys[i - 1].y < 13) ys[i].y = ys[i - 1].y + 13
    return Object.fromEntries(ys.map((v) => [v.key, v.y]))
  })

  function move(e) {
    if (points.length < 2) return
    const rect = e.currentTarget.getBoundingClientRect()
    const px = e.clientX - rect.left - pad.l
    hover = Math.max(0, Math.min(points.length - 1, Math.round((px / innerW) * (points.length - 1))))
  }
  const last = $derived(points.length ? points[points.length - 1] : null)
</script>

<div class="tc" bind:clientWidth={width} style="height: {height}px">
  {#if points.length < 2}
    <div class="tc-empty muted small">{t('Collecting data… the first points appear within a minute.')}</div>
  {:else}
    <svg {width} {height} role="img" aria-label={t('{series} over time', { series: series.map((s) => s.label).join(` ${t('and')} `) })}
      onpointermove={move} onpointerleave={() => (hover = null)}>
      {#if !compact}
        {#each [0.25, 0.5, 0.75, 1] as f}
          <line x1={pad.l} x2={pad.l + innerW} y1={pad.t + innerH * (1 - f)} y2={pad.t + innerH * (1 - f)} stroke="var(--grid)" />
        {/each}
        <line x1={pad.l} x2={pad.l + innerW} y1={pad.t + innerH} y2={pad.t + innerH} stroke="var(--border-strong)" />
        {#each ticks as i, n (n)}
          <text x={x(i)} y={height - 6} fill="var(--muted)" font-size="10.5"
            text-anchor={n === 0 ? 'start' : i === points.length - 1 ? 'end' : 'middle'}>{i === points.length - 1 ? t('now') : fmtTime(points[i].t)}</text>
        {/each}
      {/if}
      {#each series as s (s.key)}
        <path d={area(s.key)} fill={s.color} opacity="0.12" />
        <path d={line(s.key)} fill="none" stroke={s.color} stroke-width="2" stroke-linejoin="round" />
      {/each}
      {#if !compact && last}
        <!-- direct labels at the end of each line (identity isn't color-only) -->
        {#each series as s (s.key)}
          <text x={pad.l + innerW + 8} y={endY[s.key]} fill="var(--text-2)" font-size="11">{s.label}</text>
        {/each}
      {/if}
      {#if hover !== null}
        <line x1={x(hover)} x2={x(hover)} y1={pad.t} y2={pad.t + innerH} stroke="var(--muted)" stroke-dasharray="3 3" />
        {#each series as s (s.key)}
          <circle cx={x(hover)} cy={y(points[hover][s.key])} r="4" fill={s.color} stroke="var(--surface)" stroke-width="2" />
        {/each}
      {/if}
    </svg>
    {#if hover !== null}
      <div class="tc-tip" style="left: {Math.min(x(hover) + 12, width - 170)}px">
        <div class="muted">{new Date(points[hover].t * 1000).toLocaleString([], { weekday: 'short', hour: '2-digit', minute: '2-digit' })}</div>
        {#each series as s (s.key)}
          <div class="tc-row"><span class="tc-key" style="background: {s.color}"></span>{s.label}<strong>{format(points[hover][s.key] ?? 0)}</strong></div>
        {/each}
      </div>
    {/if}
  {/if}
</div>

<style>
  .tc { position: relative; width: 100%; }
  .tc svg { display: block; overflow: visible; touch-action: none; }
  .tc-empty { height: 100%; display: grid; place-items: center; border: 1px dashed var(--border); border-radius: var(--radius-sm); }
  .tc-tip { position: absolute; top: 4px; min-width: 150px; padding: 0.45rem 0.6rem; border-radius: 8px; background: var(--surface-3);
    border: 1px solid var(--border-strong); font-size: 0.78rem; pointer-events: none; box-shadow: 0 6px 18px rgb(0 0 0 / 0.3); }
  .tc-row { display: flex; align-items: center; gap: 0.4rem; margin-top: 0.2rem; }
  .tc-row strong { margin-left: auto; font-variant-numeric: tabular-nums; }
  .tc-key { width: 10px; height: 3px; border-radius: 2px; }
</style>
