<script>
  // A single-value ring (storage used). One value = no legend needed.
  let { percent = 0, size = 112, warn = 85, bad = 95 } = $props()
  const r = 44
  const c = 2 * Math.PI * r
  let clamped = $derived(Math.max(0, Math.min(100, percent)))
  let color = $derived(clamped >= bad ? 'var(--bad)' : clamped >= warn ? 'var(--warn)' : 'var(--accent)')
</script>

<svg width={size} height={size} viewBox="0 0 100 100" role="img" aria-label="{Math.round(clamped)}% used">
  <circle cx="50" cy="50" r={r} fill="none" stroke="var(--surface-3)" stroke-width="9" />
  {#if clamped >= 0.5}<!-- below that, the rounded cap alone would draw a dot -->
    <circle cx="50" cy="50" r={r} fill="none" stroke={color} stroke-width="9" stroke-linecap="round"
      stroke-dasharray="{(c * clamped) / 100} {c}" transform="rotate(-90 50 50)" />
  {/if}
  <text x="50" y="50" text-anchor="middle" dominant-baseline="central" fill="var(--text)" font-size="20" font-weight="600">{Math.round(clamped)}%</text>
  <text x="50" y="66" text-anchor="middle" fill="var(--muted)" font-size="8">used</text>
</svg>
