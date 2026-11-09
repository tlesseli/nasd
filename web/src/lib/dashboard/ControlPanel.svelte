<script>
  // Dashboard style "Control panel": the NAS as an industrial machine panel.
  // Every instrument shows live data; actions (shutdown, restart) need a
  // 3-second hold, like a real machine's stop button.
  import { onMount } from 'svelte'
  import { api, formatBytes } from '../../api.js'
  import { createDashboardData, pools, latest, cpuTemp, memPercent, diskState, bays, uptimeParts, alarms } from './data.svelte.js'

  let { onSessionLost } = $props()
  const { d, start } = createDashboardData(onSessionLost)
  onMount(start)

  // ---- instruments
  const m = $derived(latest(d.metrics))
  const netPeak = $derived(Math.max(1e6, ...d.metrics.map((p) => p.netRx ?? 0)))
  const instruments = $derived([
    { label: 'CPU load', value: m?.cpu ?? 0, max: 100, text: `${String(Math.round(m?.cpu ?? 0)).padStart(3, '0')} %`, warn: 70, crit: 90 },
    { label: 'Memory', value: memPercent(d.sys), max: 100, text: `${String(Math.round(memPercent(d.sys))).padStart(3, '0')} %`, warn: 80, crit: 92 },
    { label: 'CPU temp', value: cpuTemp(d.sensors), max: 100, text: `${String(Math.round(cpuTemp(d.sensors))).padStart(3, '0')} °C`, warn: 70, crit: 85 },
    { label: 'Net in', value: (m?.netRx ?? 0) / 1e6, max: Math.max(10, Math.ceil(netPeak / 1e6 / 10) * 10),
      text: `${((m?.netRx ?? 0) / 1e6).toFixed(1)} MB/S`, warn: Infinity, crit: Infinity },
  ])
  // A dial: 270° sweep from bottom-left, ticks, green/amber/red bands.
  const A0 = 135, SWEEP = 270
  const ang = (v, max) => A0 + (Math.min(max, Math.max(0, v)) / max) * SWEEP
  const pt = (a, r) => [70 + r * Math.cos((a * Math.PI) / 180), 70 + r * Math.sin((a * Math.PI) / 180)]
  function band(from, to, max, r = 56) {
    const [x1, y1] = pt(ang(from, max), r), [x2, y2] = pt(ang(to, max), r)
    const large = ang(to, max) - ang(from, max) > 180 ? 1 : 0
    return `M${x1} ${y1} A${r} ${r} 0 ${large} 1 ${x2} ${y2}`
  }

  // ---- annunciator
  const al = $derived(alarms(d))
  let lampTest = $state(false)
  let acked = $state('')
  const problemKey = $derived(d.problems.map((p) => p.key).join('|'))
  const lamps = $derived([
    { label: 'Disk fault', on: al.diskFault, color: 'amber' },
    { label: 'Volume degraded', on: al.poolDegraded, color: 'red' },
    { label: 'Sync running', on: al.sync, color: 'amber' },
    { label: 'Overtemp', on: al.overtemp, color: 'red' },
    { label: 'Net link', on: al.netLink, color: 'green' },
    { label: 'UPS on batt', on: al.onBattery, color: 'red' },
    { label: 'On mains', on: al.onMains, color: 'green' },
    { label: 'Backup fail', on: al.backupFail, color: 'amber' },
  ])
  function testLamps() {
    lampTest = true
    setTimeout(() => (lampTest = false), 2500)
  }

  // ---- storage and bays
  const ps = $derived(pools(d.volumes))
  const bayList = $derived(bays(d.disks))
  const hottest = $derived(d.disks.reduce((acc, x) => (x.health?.temperatureC ?? 0) > acc.t ? { t: x.health.temperatureC, bay: x.bay } : acc, { t: 0, bay: 0 }))
  const fans = $derived((d.hardware?.outputs ?? []).filter((o) => o.fanRpm > 0))
  const SEGS = 20

  // ---- services (shown, changed in System Settings → Services)
  const switches = $derived(d.services.filter((s) => s.unit !== 'ups').map((s) => ({
    label: { smbd: 'SMB', wsdd2: 'WSD', 'avahi-daemon': 'Bonjour', vsftpd: 'FTP', rsync: 'rsync' }[s.unit] ?? s.label, on: s.active,
  })))

  // ---- counters
  const up = $derived(uptimeParts(d.sys?.uptimeSeconds))
  const servedMB = $derived(Math.round(d.metrics.reduce((n, p, i, a) => n + (p.netTx ?? 0) * (i ? p.t - a[i - 1].t : 0), 0) / 1e6))
  const backupsOK = $derived(d.backups.filter((b) => b.lastRun?.status === 'done').length)
  const digits = (n, w) => String(Math.max(0, n)).padStart(w, '0').slice(-w).split('')

  // ---- power: hold 3 s to confirm
  const HOLD = 3000
  let holding = $state(null) // 'poweroff' | 'reboot'
  let progress = $state(0)
  let powerMsg = $state('')
  let timer
  function press(action) {
    holding = action
    const t0 = Date.now()
    timer = setInterval(async () => {
      progress = Math.min(1, (Date.now() - t0) / HOLD)
      if (progress >= 1) {
        release()
        try {
          await api.post('/api/system/power', { action, force: false })
          powerMsg = action === 'reboot' ? 'Restarting… this page reconnects when the NAS is back.' : 'Shutting down…'
        } catch (e) {
          powerMsg = e.message
        }
      }
    }, 50)
  }
  function release() {
    clearInterval(timer)
    holding = null
    progress = 0
  }
</script>

<div class="cp">
  <header class="plate">
    <div>
      <div class="title">{(d.sys?.hostname ?? 'NAS').toUpperCase()}</div>
      <div class="sub">Storage control panel · ReadyNAS Ultra 4 · nasd {d.sys?.version ?? ''}</div>
    </div>
    <div class="sysled"><span class="led" class:green={d.loaded && !d.error} class:red={!!d.error}></span>{d.error ? 'NO SIGNAL' : 'SYSTEM ON'}</div>
    <nav>
      <a class="key on" href="#/">Overview</a>
      <a class="key" href="#/volumes">Storage</a>
      <a class="key" href="#/shares">Shares</a>
      <a class="key" href="#/users">Users</a>
      <a class="key" href="#/system">Setup</a>
    </nav>
  </header>

  <div class="grid">
    <section class="panel instruments">
      <h3 class="tag">Primary instruments</h3>
      <div class="dials">
        {#each instruments as g (g.label)}
          {@const max = g.max}
          {@const needle = pt(ang(g.value, max), 44)}
          <figure class="dial">
            <svg viewBox="0 0 140 140" role="img" aria-label="{g.label}: {g.text}">
              <circle cx="70" cy="70" r="66" class="bezel" /><circle cx="70" cy="70" r="60" class="face" />
              <path d={band(0, Math.min(g.warn, max), max)} class="b-green" />
              {#if g.warn < max}<path d={band(g.warn, Math.min(g.crit, max), max)} class="b-amber" />{/if}
              {#if g.crit < max}<path d={band(g.crit, max, max)} class="b-red" />{/if}
              {#each Array(11) as _, i}
                {@const a = A0 + (i * SWEEP) / 10}
                {@const [x1, y1] = pt(a, 50)}
                {@const [x2, y2] = pt(a, i % 5 ? 46 : 42)}
                <line {x1} {y1} {x2} {y2} class="tick" />
              {/each}
              <line x1="70" y1="70" x2={needle[0]} y2={needle[1]} class="needle" />
              <circle cx="70" cy="70" r="6" class="hub" />
            </svg>
            <div class="readout">{g.text}</div>
            <figcaption class="tag small">{g.label}</figcaption>
          </figure>
        {/each}
      </div>
    </section>

    <section class="panel annunciator">
      <h3 class="tag">Alarm annunciator</h3>
      <div class="lamps">
        {#each lamps as l (l.label)}
          <div class="lamp {l.color}" class:lit={l.on || lampTest} class:flash={l.on && l.color !== 'green' && acked !== problemKey}>{l.label}</div>
        {/each}
      </div>
      <div class="log mono">
        {#each d.problems.slice(0, 4) as p (p.key)}<div class:crit={p.severity === 'critical'}>&gt; {p.subject}</div>
        {:else}<div class="ok">&gt; All systems normal</div>{/each}
      </div>
      <div class="buttons">
        <button class="mush yellow" onclick={() => (acked = problemKey)} aria-label="Acknowledge alarms"></button>
        <button class="mush white" onclick={testLamps} aria-label="Lamp test"></button>
        <a class="mush blue" href="#/system/events" aria-label="Event log"></a>
        <span class="tag small">ACK</span><span class="tag small">Lamp test</span><span class="tag small">Log</span>
      </div>
    </section>

    <section class="panel levels">
      <h3 class="tag">Volume levels</h3>
      <div class="meters">
        {#each ps as v (v.name)}
          {@const pct = v.totalBytes ? (100 * v.usedBytes) / v.totalBytes : 0}
          <div class="meter">
            <div class="scale"><span>100</span><span>75</span><span>50</span><span>25</span><span>0</span></div>
            <div class="bar" role="img" aria-label="{v.name}: {Math.round(pct)}% used">
              {#each Array(SEGS) as _, i}{@const lvl = (SEGS - i) * (100 / SEGS)}<span class:on={pct >= lvl - 100 / SEGS / 2} class:amber={lvl > 75} class:red={lvl > 90}></span>{/each}
            </div>
            <div class="readout small">{Math.round(pct)} %</div>
            <div class="tag small">{v.name}</div>
            <div class="note">{formatBytes(v.usedBytes)} / {formatBytes(v.totalBytes || v.sizeBytes)}</div>
          </div>
        {:else}<p class="note">No volume mounted.</p>{/each}
      </div>
    </section>

    <section class="panel bays">
      <h3 class="tag">Drive bays</h3>
      <div class="legend"><span><i class="led green"></i>Health OK</span><span><i class="led amber"></i>Fault</span><span><i class="led blue"></i>Spinning</span></div>
      <div class="rack">
        {#each bayList as b (b.n)}
          {@const st = diskState(b.disk)}
          <a class="sled" class:empty={!b.disk} class:fault={st === 'fault' || st === 'warn'} href="#/volumes">
            <div class="num">{b.n}</div>
            <div class="grille">{#each Array(7) as _}<span></span>{/each}</div>
            <div class="leds">
              <i class="led" class:green={st === 'ok'} class:amber={st === 'fault' || st === 'warn'}></i>
              <i class="led" class:blue={b.disk && !b.disk.health?.standby}></i>
            </div>
            <div class="cap">{b.disk ? formatBytes(b.disk.sizeBytes) : 'EMPTY'}</div>
            {#if b.disk?.health?.temperatureC}<div class="note">{b.disk.health.temperatureC} °C</div>{/if}
          </a>
        {/each}
      </div>
      <div class="foot mono">
        <span>Temp max {hottest.t || '—'} °C{hottest.bay ? ` · bay ${hottest.bay}` : ''}</span>
        <span>{fans.map((f, i) => `Fan ${i + 1} ${f.fanRpm} RPM`).join(' · ') || 'Fan —'}</span>
      </div>
    </section>

    <section class="panel switches">
      <h3 class="tag">Service switches</h3>
      <div class="sw-row">
        {#each switches as s (s.label)}
          <a class="sw" href="#/system/services" title="Change in System Settings → Services">
            <span class="sw-on">ON</span>
            <span class="toggle" class:up={s.on}><span class="lever"></span></span>
            <span class="sw-on">OFF</span>
            <i class="led" class:green={s.on}></i>
            <span class="tag small">{s.label}</span>
          </a>
        {/each}
      </div>
    </section>

    <section class="panel counters">
      <h3 class="tag">Counters</h3>
      <div class="ctr-label">Uptime D : H : M</div>
      <div class="digits">
        {#each digits(up.d, 3) as c}<span>{c}</span>{/each}<b>:</b>{#each digits(up.h, 2) as c}<span>{c}</span>{/each}<b>:</b>{#each digits(up.m, 2) as c}<span>{c}</span>{/each}
      </div>
      <div class="ctr-label">Data served · last hour MB</div>
      <div class="digits">{#each digits(servedMB, 6) as c}<span>{c}</span>{/each}</div>
      <div class="ctr-label">Backup jobs OK</div>
      <div class="digits">{#each digits(backupsOK, 3) as c}<span>{c}</span>{/each}</div>
    </section>

    <section class="panel power">
      <h3 class="tag">Power</h3>
      <div class="pw">
        <div class="stop-wrap">
          <button class="stop" style="--p: {holding === 'poweroff' ? progress : 0}"
            onpointerdown={() => press('poweroff')} onpointerup={release} onpointerleave={release}
            onkeydown={(e) => e.key === ' ' && !holding && press('poweroff')} onkeyup={release} aria-label="Shut down: hold 3 seconds">STOP</button>
          <span class="tag">Shutdown</span>
        </div>
        <div class="stop-wrap">
          <button class="restart" style="--p: {holding === 'reboot' ? progress : 0}"
            onpointerdown={() => press('reboot')} onpointerup={release} onpointerleave={release}
            onkeydown={(e) => e.key === ' ' && !holding && press('reboot')} onkeyup={release} aria-label="Restart: hold 3 seconds">↻</button>
          <span class="tag">Restart</span>
        </div>
      </div>
      <p class="note center">{powerMsg || 'Hold 3 s to confirm · connected computers lose their shares'}</p>
    </section>
  </div>
</div>

<style>
  .cp { --metal: #23262b; --metal-2: #2b2f35; --plate: #ddd8c4; --ink: #1d1f22; --label: #e7e2cc; --lcd: #47e07a;
    --amber: #f6b52e; --red: #e5483f; --green: #3fd16a; --blue: #3b8de8;
    background: #1a1c20; color: #d9dde3; padding: 14px; border-radius: 10px; font-family: ui-sans-serif, system-ui, sans-serif; }
  .mono, .readout, .digits, .log { font-family: ui-monospace, 'DejaVu Sans Mono', monospace; }
  .plate { display: flex; align-items: center; gap: 1.2rem; flex-wrap: wrap; background: var(--plate); color: var(--ink);
    border-radius: 6px; padding: 12px 18px; box-shadow: inset 0 0 0 2px #c9c3ac; }
  .title { font-weight: 800; font-size: 1.5rem; letter-spacing: 0.18em; }
  .sub { font-size: 0.7rem; letter-spacing: 0.2em; text-transform: uppercase; opacity: 0.75; }
  .sysled { display: flex; align-items: center; gap: 0.5rem; background: #1b1d20; color: #e9edf2; padding: 0.45rem 0.8rem; border-radius: 4px;
    font-size: 0.72rem; letter-spacing: 0.2em; font-weight: 700; }
  nav { margin-left: auto; display: flex; gap: 6px; flex-wrap: wrap; }
  .key { background: #2a2d32; color: #e8ebef; text-decoration: none; padding: 0.7rem 1.1rem; border-radius: 4px; font-weight: 800;
    font-size: 0.72rem; letter-spacing: 0.18em; text-transform: uppercase; box-shadow: inset 0 -3px 0 #0006; }
  .key.on { background: var(--amber); color: #2a1d00; }
  .grid { display: grid; grid-template-columns: repeat(12, 1fr); gap: 14px; margin-top: 14px; }
  .panel { background: var(--metal); border-radius: 6px; padding: 14px; box-shadow: inset 0 0 0 1px #ffffff0d, 0 1px 0 #0008; min-width: 0; }
  .instruments { grid-column: span 8; } .annunciator { grid-column: span 4; }
  .levels { grid-column: span 4; } .bays { grid-column: span 8; }
  .switches { grid-column: span 5; } .counters { grid-column: span 4; } .power { grid-column: span 3; }
  @media (max-width: 1100px) { .panel { grid-column: span 12 !important; } }
  .tag { display: inline-block; background: var(--label); color: var(--ink); font-size: 0.68rem; font-weight: 800; letter-spacing: 0.22em;
    text-transform: uppercase; padding: 0.22rem 0.55rem; border-radius: 2px; margin: 0; }
  .tag.small { font-size: 0.6rem; letter-spacing: 0.16em; }
  .note { font-size: 0.72rem; color: #9aa1aa; margin-top: 0.3rem; }
  .center { text-align: center; }
  /* dials */
  .dials { display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); gap: 1rem; margin-top: 1rem; }
  .dial { margin: 0; display: flex; flex-direction: column; align-items: center; gap: 0.5rem; }
  .dial svg { width: 100%; max-width: 180px; }
  .bezel { fill: #3a3e44; stroke: #555b63; stroke-width: 2; } .face { fill: #0d0f11; }
  .b-green, .b-amber, .b-red { fill: none; stroke-width: 7; stroke-linecap: butt; }
  .b-green { stroke: var(--green); } .b-amber { stroke: var(--amber); } .b-red { stroke: var(--red); }
  .tick { stroke: #e8ecef; stroke-width: 1.6; }
  .needle { stroke: var(--red); stroke-width: 3; stroke-linecap: round; transition: all 0.8s; }
  .hub { fill: #555b63; stroke: #0d0f11; stroke-width: 2; }
  .readout { background: #07130b; color: var(--lcd); border: 1px solid #1f3a27; padding: 0.3rem 0.9rem; border-radius: 3px;
    font-size: 1.2rem; letter-spacing: 0.08em; text-shadow: 0 0 6px #47e07a66; }
  .readout.small { font-size: 1rem; }
  /* annunciator */
  .lamps { display: grid; grid-template-columns: 1fr 1fr; gap: 6px; margin-top: 1rem; background: #111316; padding: 6px; border-radius: 4px; }
  .lamp { padding: 0.9rem 0.4rem; text-align: center; font-size: 0.66rem; font-weight: 800; letter-spacing: 0.16em; text-transform: uppercase;
    background: #2b2e33; color: #8d939b; border-radius: 2px; }
  .lamp.lit.amber { background: var(--amber); color: #2b1b00; box-shadow: 0 0 14px #f6b52e55; }
  .lamp.lit.red { background: var(--red); color: #fff; box-shadow: 0 0 14px #e5483f66; }
  .lamp.lit.green { background: var(--green); color: #052b10; box-shadow: 0 0 14px #3fd16a55; }
  .lamp.flash { animation: flash 1s steps(2) infinite; }
  @keyframes flash { 50% { filter: brightness(0.55); } }
  @media (prefers-reduced-motion: reduce) { .lamp.flash { animation: none; } }
  .log { margin-top: 10px; background: #07130b; border: 1px solid #1f3a27; color: #c9d64a; font-size: 0.74rem; padding: 0.6rem 0.8rem;
    border-radius: 3px; min-height: 4.2rem; }
  .log .crit { color: #ff8a7a; } .log .ok { color: var(--lcd); }
  .buttons { display: grid; grid-template-columns: repeat(3, 1fr); justify-items: center; gap: 0.4rem; margin-top: 12px; }
  .mush { width: 52px; height: 52px; border-radius: 50%; border: 4px solid #3a3e44; cursor: pointer; box-shadow: inset 0 -5px 8px #0005; }
  .mush.yellow { background: radial-gradient(circle at 40% 35%, #ffe08a, #e8a91a); }
  .mush.white { background: radial-gradient(circle at 40% 35%, #fff, #cfcabb); }
  .mush.blue { background: radial-gradient(circle at 40% 35%, #8cc0ff, #2b6fd6); display: block; }
  /* levels */
  .meters { display: flex; gap: 2rem; justify-content: center; flex-wrap: wrap; margin-top: 1rem; }
  .meter { display: grid; grid-template-columns: auto auto; gap: 0.4rem 0.5rem; justify-items: center; }
  .meter .readout, .meter .tag, .meter .note { grid-column: 1 / -1; }
  .scale { display: flex; flex-direction: column; justify-content: space-between; font-size: 0.6rem; color: #9aa1aa; height: 230px; }
  .bar { display: flex; flex-direction: column; gap: 2px; background: #0d0f11; padding: 5px; border: 2px solid #3a3e44; border-radius: 3px; width: 56px; }
  .bar span { height: 9px; background: #1e2a21; border-radius: 1px; }
  .bar span.on { background: var(--green); box-shadow: 0 0 4px #3fd16a66; }
  .bar span.on.amber { background: var(--amber); } .bar span.on.red { background: var(--red); }
  /* bays */
  .legend { float: right; display: flex; gap: 0.9rem; font-size: 0.66rem; letter-spacing: 0.12em; text-transform: uppercase; color: #9aa1aa; }
  .legend i { margin-right: 0.35rem; }
  .rack { clear: both; display: grid; grid-template-columns: repeat(4, 1fr); gap: 8px; margin-top: 1rem; background: #111316; padding: 8px; border-radius: 4px;
    border: 2px solid #3a3e44; }
  .sled { display: flex; flex-direction: column; align-items: center; gap: 0.7rem; padding: 0.9rem 0.4rem; background: #3a3e44; border-radius: 3px;
    text-decoration: none; color: inherit; border: 2px solid transparent; }
  .sled.fault { border-color: var(--amber); background: #3d3424; }
  .sled.empty { background: #15171a; color: #6b7079; }
  .num { font-size: 1.1rem; }
  .grille { display: flex; flex-direction: column; gap: 3px; width: 60%; }
  .grille span { height: 3px; background: #101215; border-radius: 1px; }
  .sled.empty .grille { visibility: hidden; }
  .leds { display: flex; gap: 0.5rem; }
  .led { display: inline-block; width: 9px; height: 9px; border-radius: 50%; background: #2a2d32; box-shadow: inset 0 0 0 1px #0006; }
  .led.green { background: var(--green); box-shadow: 0 0 6px var(--green); }
  .led.amber { background: var(--amber); box-shadow: 0 0 6px var(--amber); }
  .led.red { background: var(--red); box-shadow: 0 0 6px var(--red); }
  .led.blue { background: var(--blue); box-shadow: 0 0 6px var(--blue); }
  .cap { font-size: 0.72rem; letter-spacing: 0.12em; }
  .foot { display: flex; justify-content: space-between; flex-wrap: wrap; gap: 0.5rem; margin-top: 0.7rem; font-size: 0.72rem; color: #b6bcc4;
    text-transform: uppercase; letter-spacing: 0.08em; }
  /* switches */
  .sw-row { display: flex; flex-wrap: wrap; gap: 1.2rem; margin-top: 1rem; }
  .sw { display: flex; flex-direction: column; align-items: center; gap: 0.35rem; text-decoration: none; color: inherit; }
  .sw-on { font-size: 0.6rem; letter-spacing: 0.15em; color: #9aa1aa; }
  .toggle { position: relative; width: 44px; height: 62px; background: #8e949b; border-radius: 4px; box-shadow: inset 0 0 0 3px #6b7178; }
  .lever { position: absolute; left: 50%; top: 50%; width: 16px; height: 26px; margin-left: -8px; background: #f2f2ef; border-radius: 8px;
    border: 3px solid #1b1d20; transform: translateY(2px); transition: transform 0.2s; }
  .toggle.up .lever { transform: translateY(-26px); }
  /* counters */
  .ctr-label { font-size: 0.68rem; letter-spacing: 0.16em; text-transform: uppercase; color: #c3c8cf; margin: 1rem 0 0.4rem; }
  .digits { display: flex; align-items: center; gap: 4px; }
  .digits span { background: #0d0f11; border: 1px solid #3a3e44; padding: 0.35rem 0.5rem; font-size: 1.3rem; border-radius: 2px; color: #f1f3f5; }
  .digits b { color: #9aa1aa; }
  /* power */
  .pw { display: flex; gap: 1.2rem; align-items: center; justify-content: center; margin-top: 1rem; flex-wrap: wrap; }
  .stop-wrap { display: flex; flex-direction: column; align-items: center; gap: 0.6rem; }
  .stop { width: 110px; height: 110px; border-radius: 50%; border: 10px solid; cursor: pointer; font-weight: 900; color: #fff; letter-spacing: 0.12em;
    border-image: none; background: radial-gradient(circle at 40% 35%, #ff7a6b, #c9231b);
    box-shadow: 0 0 0 10px #f2c21b, 0 0 0 calc(10px + 12px * var(--p)) #ffffff22, inset 0 -8px 12px #0006;
    border-color: #1b1d20; outline-offset: 16px; }
  .restart { width: 64px; height: 64px; border-radius: 50%; border: 4px solid #3a3e44; background: #2a2d32; color: var(--amber); font-size: 1.8rem;
    cursor: pointer; box-shadow: 0 0 0 calc(10px * var(--p)) #ffffff22; }
</style>
