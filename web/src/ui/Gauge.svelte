<script>
  // A half-circle gauge: an arc from min to max, the value as a filled arc,
  // and short ticks for limits (each with its own small label underneath).
  let { value = 0, min = 0, max = 100, label = '', text = '', marks = [], warn = false } = $props()

  const R = 52, CX = 64, CY = 64, W = 8
  const angle = (v) => Math.PI * (1 - (Math.min(max, Math.max(min, v)) - min) / (max - min || 1))
  const point = (a, r = R) => [CX + r * Math.cos(a), CY - r * Math.sin(a)]
  function arc(from, to) {
    const [x1, y1] = point(angle(from)), [x2, y2] = point(angle(to))
    return `M${x1.toFixed(1)} ${y1.toFixed(1)} A${R} ${R} 0 0 1 ${x2.toFixed(1)} ${y2.toFixed(1)}`
  }
</script>

<figure class="gauge" aria-label="{label}: {text}">
  <svg viewBox="0 0 128 74" role="img" aria-hidden="true">
    <path d={arc(min, max)} class="track" stroke-width={W} />
    {#if value > min}<path d={arc(min, value)} class="fill" class:warn stroke-width={W} />{/if}
    {#each marks as m}
      {@const a = angle(m.value)}
      {@const [x1, y1] = point(a, R - W / 2 - 3)}
      {@const [x2, y2] = point(a, R + W / 2 + 3)}
      <line {x1} {y1} {x2} {y2} class="mark" />
    {/each}
  </svg>
  <div class="value">{text}</div>
  <figcaption>{label}</figcaption>
  {#if marks.length}
    <ul class="legend">{#each marks as m}<li><span class="tick"></span>{m.label}</li>{/each}</ul>
  {/if}
</figure>

<style>
  .gauge { margin: 0; display: flex; flex-direction: column; align-items: center; min-width: 170px; }
  svg { width: 100%; max-width: 200px; overflow: visible; }
  .track { fill: none; stroke: var(--surface-3); stroke-linecap: round; }
  .fill { fill: none; stroke: var(--accent); stroke-linecap: round; transition: d 0.6s; }
  .fill.warn { stroke: var(--warn); }
  .mark { stroke: var(--text); stroke-width: 2; stroke-linecap: round; }
  .value { margin-top: -1.9rem; font-size: 1.35rem; font-weight: 600; font-variant-numeric: tabular-nums; }
  figcaption { font-size: 0.8rem; color: var(--muted); margin-top: 0.1rem; }
  .legend { list-style: none; margin: 0.45rem 0 0; padding: 0; font-size: 0.75rem; color: var(--muted); text-align: center; }
  .tick { display: inline-block; width: 2px; height: 9px; background: var(--text); margin-right: 0.35rem; vertical-align: -1px; }
</style>
