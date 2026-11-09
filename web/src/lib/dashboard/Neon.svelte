<script>
  // Dashboard style "Neon": a dark, glowing view of the same live data.
  import { onMount } from 'svelte'
  import { formatBytes } from '../../api.js'
  import { createDashboardData, pools, latest, memPercent, diskState, bays, uptimeParts, alarms } from './data.svelte.js'

  let { user, onSessionLost } = $props()
  const { d, start } = createDashboardData(onSessionLost)
  onMount(start)

  const m = $derived(latest(d.metrics))
  const cpu = $derived(Math.round(m?.cpu ?? 0))
  const mem = $derived(Math.round(memPercent(d.sys)))
  const memUsed = $derived(d.sys ? d.sys.memTotalBytes - d.sys.memAvailableBytes : 0)
  const up = $derived(uptimeParts(d.sys?.uptimeSeconds))
  const al = $derived(alarms(d))
  const healthy = $derived(!al.diskFault && !al.poolDegraded && !al.overtemp && !d.error)

  // ---- the main volume
  const vol = $derived(pools(d.volumes).sort((a, b) => (b.totalBytes || b.sizeBytes) - (a.totalBytes || a.sizeBytes))[0])
  const vTotal = $derived(vol ? vol.totalBytes || vol.sizeBytes : 0)
  const vUsed = $derived(vol?.usedBytes ?? 0)
  const levelText = { raid1: 'Mirror', raid5: 'Distributed parity', raid6: 'Double parity', raid10: 'Striped mirrors', raid0: 'Striped' }
  const layers = $derived(Math.max(2, vol?.members.length ?? 4))

  // ---- rings
  const RING = 2 * Math.PI * 52
  const ring = (pct) => `${(RING * Math.min(100, Math.max(0, pct))) / 100} ${RING}`
  const SMALL = 2 * Math.PI * 30
  const small = (pct) => `${(SMALL * Math.min(100, Math.max(0, pct))) / 100} ${SMALL}`

  // ---- network chart (last hour)
  const W = 340, H = 120
  const pts = $derived(d.metrics.slice(-60))
  const peak = $derived(Math.max(1e5, ...pts.map((p) => Math.max(p.netRx ?? 0, p.netTx ?? 0))))
  function line(key) {
    if (pts.length < 2) return ''
    return pts.map((p, i) => `${i ? 'L' : 'M'}${((i / (pts.length - 1)) * W).toFixed(1)} ${(H - ((p[key] ?? 0) / peak) * (H - 8)).toFixed(1)}`).join(' ')
  }
  const rate = (b) => (b >= 1e6 ? `${(b / 1e6).toFixed(1)} MB/s` : `${Math.round((b ?? 0) / 1e3)} kB/s`)

  // ---- disks
  const bayList = $derived(bays(d.disks))
</script>

<div class="neon">
  <header class="top">
    <div class="brand">
      <svg viewBox="0 0 40 40" class="logo" aria-hidden="true"><path d="M20 2 36 11v18L20 38 4 29V11z" /><path d="M12 15v10l8 5 8-5V15l-8-5z" /></svg>
      <div><div class="name">{(d.sys?.hostname ?? 'nas').toUpperCase()}</div><div class="tagline">Network · Storage · {d.sys?.version ? 'nasd ' + d.sys.version.split('+')[0] : ''}</div></div>
    </div>
    <div class="glass status"><span class="dot" class:bad={!healthy}></span><div><div class="cap">System status</div><div class="big">{d.error ? 'OFFLINE' : healthy ? 'ONLINE' : 'ATTENTION'}</div></div></div>
    <div class="glass meta">
      <div><div class="cap">Uptime</div><div class="val">{up.d}d {String(up.h).padStart(2, '0')}h {String(up.m).padStart(2, '0')}m</div></div>
      <div><div class="cap">Signed in</div><div class="val">{user ?? 'admin'}@{d.sys?.hostname ?? 'nas'}.local</div></div>
    </div>
  </header>

  <div class="grid">
    <section class="glass cpu">
      <h3>CPU usage</h3>
      <svg viewBox="0 0 140 140" class="ring" role="img" aria-label="CPU {cpu}%">
        <circle cx="70" cy="70" r="52" class="track" /><circle cx="70" cy="70" r="52" class="arc cyan" stroke-dasharray={ring(cpu)} />
        <text x="70" y="70" class="pct">{cpu}%</text><text x="70" y="90" class="sub">utilization</text>
      </svg>
      <div class="stats"><div><span>Cores</span>{d.sys?.cores ?? '—'}</div><div><span>Load 1 min</span>{d.sys?.load?.[0]?.toFixed(2) ?? '—'}</div><div><span>CPU</span>{(d.sys?.cpu ?? '').replace(/Intel\(R\) |CPU |\(TM\)/g, '').split('@')[0] || '—'}</div></div>
    </section>

    <section class="glass array">
      <h3>RAID array overview</h3>
      <svg viewBox="0 0 300 230" class="stack" aria-hidden="true">
        <defs><linearGradient id="nlayer" x1="0" x2="1"><stop offset="0" stop-color="#36d1ff" stop-opacity=".35" /><stop offset="1" stop-color="#b56cff" stop-opacity=".35" /></linearGradient></defs>
        <ellipse cx="150" cy="200" rx="120" ry="22" class="halo" /><ellipse cx="150" cy="200" rx="85" ry="15" class="halo" />
        {#each Array(layers) as _, i}
          {@const y = 170 - i * (130 / layers)}
          <g class="layer" class:warn={vol && vol.members[i] && vol.members[i].state.includes('faulty')}>
            <path d="M150 {y - 22} L250 {y - 2} L150 {y + 18} L50 {y - 2} Z" class="topf" />
            <path d="M50 {y - 2} L150 {y + 18} L150 {y + 30} L50 {y + 10} Z" class="side" />
            <path d="M250 {y - 2} L150 {y + 18} L150 {y + 30} L250 {y + 10} Z" class="side r" />
          </g>
        {/each}
      </svg>
      {#if vol}
        <div class="level">{vol.level.replace('raid', 'RAID ')} <span>|</span> {levelText[vol.level] ?? ''}</div>
        <div class="trio">
          {#each [['Used', vUsed, vTotal ? (100 * vUsed) / vTotal : 0], ['Total', vTotal, 100], ['Free', vTotal - vUsed, vTotal ? (100 * (vTotal - vUsed)) / vTotal : 0]] as [label, bytes, pct]}
            <figure>
              <svg viewBox="0 0 76 76" class="mini"><circle cx="38" cy="38" r="30" class="track" /><circle cx="38" cy="38" r="30" class="arc" class:violet={label === 'Free'} stroke-dasharray={small(pct)} />
                <text x="38" y="42" class="mv">{formatBytes(bytes).replace(' ', ' ')}</text></svg>
              <figcaption>{label} {label === 'Total' ? 'capacity' : ''}<br /><span>{Math.round(pct)}%</span></figcaption>
            </figure>
          {/each}
        </div>
        <div class="health"><span class:bad={!healthy}>Array health · {vol.status === 'healthy' ? 'optimal' : vol.status}</span>
          <small>{healthy ? 'All systems operational' : d.problems[0]?.subject ?? 'Needs attention'}</small></div>
      {:else}<p class="muted">No volume yet.</p>{/if}
    </section>

    <section class="glass net">
      <h3>Network throughput <small>last hour</small></h3>
      <div class="rates"><div><span class="cap">In</span><b class="cyan-t">{rate(m?.netRx)} ↑</b></div><div><span class="cap">Out</span><b class="violet-t">{rate(m?.netTx)} ↑</b></div></div>
      <svg viewBox="0 0 {W} {H}" class="chart" role="img" aria-label="Network in {rate(m?.netRx)}, out {rate(m?.netTx)}">
        {#each [0.25, 0.5, 0.75] as f}<line x1="0" x2={W} y1={H * f} y2={H * f} class="gridl" />{/each}
        <path d={line('netRx')} class="l cyan" /><path d={line('netTx')} class="l violet" />
      </svg>
      <div class="legend"><span><i class="cyan-b"></i>Inbound</span><span><i class="violet-b"></i>Outbound</span><span class="muted">peak {rate(peak)}</span></div>
    </section>

    <section class="glass mem">
      <h3>Memory usage</h3>
      <svg viewBox="0 0 140 140" class="ring" role="img" aria-label="Memory {mem}%">
        <circle cx="70" cy="70" r="52" class="track" /><circle cx="70" cy="70" r="52" class="arc violet" stroke-dasharray={ring(mem)} />
        <text x="70" y="70" class="pct">{mem}%</text><text x="70" y="90" class="sub">utilization</text>
      </svg>
      <div class="stats"><div><span>Used</span>{formatBytes(memUsed)}</div><div><span>Total</span>{formatBytes(d.sys?.memTotalBytes ?? 0)}</div><div><span>Available</span>{formatBytes(d.sys?.memAvailableBytes ?? 0)}</div></div>
    </section>

    <section class="glass disks">
      <h3>Disk health <small>{d.disks.filter((x) => x.bay).length} disks{vol ? ` · ${vol.level.replace('raid', 'RAID ')}` : ''}</small></h3>
      <div class="hexes">
        {#each bayList as b (b.n)}
          {@const st = diskState(b.disk)}
          <a class="hex" class:bad={st === 'fault'} class:warn={st === 'warn'} class:empty={st === 'empty'} href="#/volumes">
            <span class="hn">Disk {String(b.n).padStart(2, '0')}</span>
            <span class="ht">{b.disk?.health?.temperatureC ? `${b.disk.health.temperatureC}°C` : st === 'empty' ? 'empty' : '—'}
              {#if st === 'ok'}<b class="ok">✓</b>{:else if st === 'fault' || st === 'warn'}<b class="ko">!</b>{/if}</span>
          </a>
        {/each}
      </div>
      <div class="foot"><span>SMART status · {al.diskFault ? 'attention' : 'healthy'}</span><span>{al.diskFault ? 'Check the disks' : 'All disks operational'}</span></div>
    </section>
  </div>

  <nav class="dock glass">
    {#each [['#/volumes', 'Storage', 'M4 6h16v4H4zM4 14h16v4H4z'], ['#/shares', 'Shares', 'M7 12a3 3 0 1 0 0-.01M17 6a3 3 0 1 0 0-.01M17 18a3 3 0 1 0 0-.01M9.7 10.6l4.6-3.2M9.7 13.4l4.6 3.2'],
      ['#/users', 'Users', 'M9 11a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM3 20c0-3 3-5 6-5s6 2 6 5M16 11a3 3 0 1 0 0-6M21 20c0-2.5-1.5-4.3-4-4.9'],
      ['#/backups', 'Backup', 'M7 18a5 5 0 1 1 1-9.9A6 6 0 0 1 19 10a4 4 0 0 1-1 8zM12 16V10M9.5 12.5 12 10l2.5 2.5'],
      ['#/system/restart', 'Power', 'M12 3v8M6.3 6.3a8 8 0 1 0 11.4 0'], ['#/system', 'Settings', 'M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM19.4 13a7.5 7.5 0 0 0 0-2l2-1.6-2-3.4-2.4 1a7 7 0 0 0-1.7-1L15 3.5h-4l-.4 2.5a7 7 0 0 0-1.7 1l-2.4-1-2 3.4L6.6 11a7.5 7.5 0 0 0 0 2l-2 1.6 2 3.4 2.4-1a7 7 0 0 0 1.7 1l.4 2.5h4l.4-2.5a7 7 0 0 0 1.7-1l2.4 1 2-3.4z']] as [href, label, icon]}
      <a {href}><svg viewBox="0 0 24 24" aria-hidden="true"><path d={icon} /></svg>{label}</a>
    {/each}
  </nav>
</div>

<style>
  .neon { --cyan: #36d1ff; --violet: #b56cff; --bg: #060a18; --glass: #0b1430cc; --edge: #2a4d8f66;
    background: radial-gradient(ellipse at 50% 0%, #10204a 0%, var(--bg) 60%); color: #dce8ff; padding: 16px; border-radius: 12px;
    font-family: ui-sans-serif, system-ui, sans-serif; }
  .glass { background: var(--glass); border: 1px solid var(--edge); border-radius: 12px; box-shadow: 0 0 24px #1d4ed822, inset 0 0 20px #36d1ff0a; padding: 14px 16px; min-width: 0; }
  h3 { margin: 0 0 0.6rem; font-size: 0.85rem; letter-spacing: 0.12em; text-transform: uppercase; color: #bfe9ff; font-weight: 600; }
  h3 small { color: #7f93bf; font-size: 0.7rem; margin-left: 0.4rem; }
  .muted { color: #7f93bf; }
  .top { display: flex; gap: 14px; align-items: stretch; flex-wrap: wrap; margin-bottom: 14px; }
  .brand { display: flex; align-items: center; gap: 0.8rem; margin-right: auto; }
  .logo { width: 44px; height: 44px; fill: none; stroke: var(--cyan); stroke-width: 2.4; filter: drop-shadow(0 0 6px var(--cyan)); }
  .name { font-size: 1.7rem; font-weight: 800; letter-spacing: 0.08em; background: linear-gradient(90deg, #e8f6ff, var(--cyan)); -webkit-background-clip: text; background-clip: text; color: transparent; }
  .tagline { font-size: 0.7rem; letter-spacing: 0.3em; color: #8fb7e8; text-transform: uppercase; }
  .status { display: flex; align-items: center; gap: 0.8rem; padding: 10px 22px; }
  .dot { width: 10px; height: 10px; border-radius: 50%; background: #3ee08f; box-shadow: 0 0 10px #3ee08f; }
  .dot.bad { background: #ffb23e; box-shadow: 0 0 10px #ffb23e; }
  .cap { font-size: 0.66rem; letter-spacing: 0.16em; text-transform: uppercase; color: #8fb7e8; }
  .big { font-size: 1.25rem; font-weight: 700; letter-spacing: 0.12em; color: var(--cyan); }
  .meta { display: flex; gap: 1.6rem; padding: 10px 18px; }
  .val { font-size: 0.95rem; }
  .grid { display: grid; grid-template-columns: 1fr 1.5fr 1.2fr; grid-template-areas: "cpu array net" "mem array disks"; gap: 14px; }
  .cpu { grid-area: cpu; } .mem { grid-area: mem; } .array { grid-area: array; } .net { grid-area: net; } .disks { grid-area: disks; }
  @media (max-width: 1100px) { .grid { grid-template-columns: 1fr; grid-template-areas: "cpu" "mem" "array" "net" "disks"; } }
  .ring { width: 100%; max-width: 180px; display: block; margin: 0 auto; }
  .track { fill: none; stroke: #1a2a52; stroke-width: 9; }
  .arc { fill: none; stroke-width: 9; stroke-linecap: round; transform: rotate(-90deg); transform-origin: center; transform-box: fill-box; stroke: var(--cyan);
    filter: drop-shadow(0 0 5px var(--cyan)); transition: stroke-dasharray 0.8s; }
  .arc.violet { stroke: var(--violet); filter: drop-shadow(0 0 5px var(--violet)); }
  .pct { font-size: 26px; font-weight: 700; fill: #f2f8ff; text-anchor: middle; dominant-baseline: middle; }
  .sub { font-size: 9px; fill: #8fb7e8; text-anchor: middle; letter-spacing: 0.15em; text-transform: uppercase; }
  .stats { display: flex; justify-content: space-between; gap: 0.5rem; margin-top: 0.6rem; font-size: 0.9rem; }
  .stats div { display: flex; flex-direction: column; gap: 0.15rem; min-width: 0; overflow-wrap: anywhere; }
  .stats span { font-size: 0.62rem; letter-spacing: 0.14em; text-transform: uppercase; color: #8fb7e8; }
  .stack { width: 100%; max-height: 250px; display: block; }
  .halo { fill: none; stroke: var(--cyan); stroke-width: 1.5; opacity: 0.6; filter: drop-shadow(0 0 6px var(--cyan)); }
  .layer .topf { fill: url(#nlayer); stroke: var(--cyan); stroke-width: 1.2; filter: drop-shadow(0 0 4px #36d1ff88); }
  .layer .side { fill: #36d1ff22; stroke: var(--cyan); stroke-width: 1; }
  .layer .side.r { fill: #b56cff22; stroke: var(--violet); }
  .layer.warn .topf, .layer.warn .side { stroke: #ffb23e; }
  .level { text-align: center; letter-spacing: 0.14em; text-transform: uppercase; color: #bfe9ff; font-size: 0.85rem; margin-top: 0.3rem; }
  .level span { color: #4a6aa8; margin: 0 0.4rem; }
  .trio { display: flex; justify-content: space-around; margin-top: 0.6rem; }
  .trio figure { margin: 0; text-align: center; font-size: 0.66rem; letter-spacing: 0.12em; text-transform: uppercase; color: #8fb7e8; }
  .trio figcaption span { color: #dce8ff; }
  .mini { width: 84px; display: block; margin: 0 auto 0.3rem; }
  .mv { font-size: 10.5px; font-weight: 700; fill: #f2f8ff; text-anchor: middle; }
  .health { margin-top: 0.8rem; text-align: center; border: 1px solid var(--edge); border-radius: 10px; padding: 0.6rem; display: flex; flex-direction: column; gap: 0.2rem; }
  .health span { color: var(--cyan); letter-spacing: 0.14em; text-transform: uppercase; font-size: 0.82rem; }
  .health span.bad { color: #ffb23e; }
  .health small { color: #8fb7e8; }
  .rates { display: flex; justify-content: space-between; font-size: 0.95rem; }
  .rates div { display: flex; flex-direction: column; }
  .cyan-t { color: var(--cyan); } .violet-t { color: var(--violet); }
  .chart { width: 100%; height: auto; margin-top: 0.5rem; display: block; }
  .gridl { stroke: #1a2a52; stroke-width: 1; }
  .l { fill: none; stroke-width: 2; stroke-linejoin: round; }
  .l.cyan { stroke: var(--cyan); filter: drop-shadow(0 0 3px var(--cyan)); }
  .l.violet { stroke: var(--violet); filter: drop-shadow(0 0 3px var(--violet)); }
  .legend { display: flex; gap: 1rem; font-size: 0.7rem; letter-spacing: 0.1em; text-transform: uppercase; margin-top: 0.4rem; color: #bfe9ff; flex-wrap: wrap; }
  .legend i { display: inline-block; width: 14px; height: 2px; vertical-align: middle; margin-right: 0.35rem; }
  .cyan-b { background: var(--cyan); } .violet-b { background: var(--violet); }
  .hexes { display: grid; grid-template-columns: repeat(auto-fill, minmax(96px, 1fr)); gap: 10px; }
  .hex { aspect-ratio: 1.1; clip-path: polygon(25% 3%, 75% 3%, 100% 50%, 75% 97%, 25% 97%, 0 50%); background: linear-gradient(160deg, #36d1ff33, #b56cff22);
    display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 0.2rem; text-decoration: none; color: #dce8ff;
    box-shadow: inset 0 0 0 2px var(--cyan); }
  .hex.warn { background: linear-gradient(160deg, #ffb23e44, #b56cff22); } .hex.bad { background: #ff4d4d55; } .hex.empty { opacity: 0.35; }
  .hn { font-size: 0.72rem; letter-spacing: 0.1em; text-transform: uppercase; }
  .ht { font-size: 0.8rem; color: #8fb7e8; }
  .ok { color: #3ee08f; margin-left: 0.2rem; } .ko { color: #ffb23e; margin-left: 0.2rem; }
  .foot { display: flex; justify-content: space-between; margin-top: 0.7rem; font-size: 0.68rem; letter-spacing: 0.12em; text-transform: uppercase; color: #8fb7e8; flex-wrap: wrap; gap: 0.4rem; }
  .dock { display: flex; justify-content: space-around; flex-wrap: wrap; gap: 0.6rem; margin-top: 14px; padding: 12px; }
  .dock a { display: flex; flex-direction: column; align-items: center; gap: 0.35rem; color: #dce8ff; text-decoration: none; font-size: 0.78rem;
    letter-spacing: 0.14em; text-transform: uppercase; min-width: 84px; }
  .dock svg { width: 30px; height: 30px; fill: none; stroke: var(--cyan); stroke-width: 1.6; stroke-linecap: round; stroke-linejoin: round;
    filter: drop-shadow(0 0 4px var(--cyan)); }
  .dock a:hover svg { stroke: var(--violet); filter: drop-shadow(0 0 6px var(--violet)); }
</style>
