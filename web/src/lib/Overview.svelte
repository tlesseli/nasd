<script>
  import { onMount } from 'svelte'
  import { api, formatBytes } from '../api.js'
  import Icon from '../ui/Icon.svelte'
  import Donut from '../ui/Donut.svelte'
  import TimeChart from '../ui/TimeChart.svelte'
  import { t, N_ } from '../i18n.svelte.js'

  let { user, onSessionLost, problems = [] } = $props()

  const BAYS = 4 // the RND-4A has four drive bays
  let range = $state('24h')
  let sys = $state(null)
  let volumes = $state([])
  let disks = $state([])
  let metrics = $state([])
  let history = $state([])
  let services = $state([])
  let backups = $state([])
  let events = $state([])
  let sensors = $state([])
  let error = $state('')

  async function loadSlow() {
    try {
      ;[sys, volumes, disks, history, services, backups, events, sensors] = await Promise.all([
        api.get('/api/system'), api.get('/api/volumes'), api.get('/api/disks'), api.get('/api/storage/history'),
        api.get('/api/services'), api.get('/api/backups'), api.get('/api/events'), api.get('/api/sensors'),
      ])
    } catch (e) {
      if (e.status === 401) onSessionLost()
      else error = e.message
    }
  }
  async function loadMetrics() {
    try {
      metrics = (await api.get(`/api/metrics?range=${range}`)).points
    } catch {}
  }
  onMount(() => {
    loadSlow()
    const a = setInterval(loadSlow, 30_000)
    const b = setInterval(loadMetrics, 10_000)
    return () => (clearInterval(a), clearInterval(b))
  })
  $effect(() => {
    range
    loadMetrics()
  })

  // ---- storage
  const pools = $derived(volumes.filter((v) => !v.system))
  const used = $derived(pools.reduce((n, v) => n + (v.usedBytes || 0), 0))
  const total = $derived(pools.reduce((n, v) => n + (v.totalBytes || 0), 0))
  const growth = $derived.by(() => {
    if (history.length < 2) return null
    const sum = (p) => Object.values(p.used ?? {}).reduce((a, b) => a + b, 0)
    const last = history[history.length - 1]
    const weekAgo = history.find((p) => p.t >= last.t - 7 * 86400) ?? history[0]
    const span = last.t - weekAgo.t
    if (span < 86400) return null // less than a day of history: no trend yet
    const perWeek = ((sum(last) - sum(weekAgo)) / span) * 7 * 86400
    const months = perWeek > 0 ? (total - used) / perWeek / 4.345 : null
    return { perWeek, months }
  })

  // ---- metrics
  const latest = $derived(metrics[metrics.length - 1])
  const fmtRate = (b) => (b >= 1e6 ? `${(b / 1e6).toFixed(b >= 1e7 ? 0 : 1)} MB/s` : `${Math.round(b / 1e3)} kB/s`)
  const peak = (key) => metrics.reduce((m, p) => Math.max(m, p[key] ?? 0), 0)
  const cpuTemp = $derived(sensors.filter((s) => s.kind === 'temp' && /coretemp|k10temp|cpu/i.test(s.chip + s.label))
    .reduce((m, s) => Math.max(m, s.value), 0))
  const timeFormat = $derived(range === '7d' || range === '30d'
    ? (t) => new Date(t * 1000).toLocaleDateString([], { day: 'numeric', month: 'short' })
    : undefined)

  // ---- attention
  function reviewLink(kind) {
    if (/^(raid|disk|volume|scrub)/.test(kind)) return '#/volumes'
    if (/^ups/.test(kind)) return '#/system/power'
    if (/fan|system_hot/.test(kind)) return '#/system/hardware'
    if (kind === 'quota') return '#/users'
    if (kind === 'backup_failed') return '#/backups'
    return '#/system/events'
  }
  const top = $derived(problems[0])

  // ---- bays
  const bays = $derived(Array.from({ length: BAYS }, (_, i) => ({ n: i + 1, disk: disks.find((d) => d.bay === i + 1) })))
  const volumeOf = $derived(Object.fromEntries(pools.flatMap((v) => v.members.map((m) => [m.disk, v.name]))))
  function diskState(d) {
    const h = d?.health
    if (!h || !h.supported) return 'off'
    if (!h.passed) return 'bad'
    if (h.reallocatedSectors + h.pendingSectors + h.uncorrectableSectors > 0) return 'warn'
    return ''
  }

  // ---- misc
  const greeting = $derived.by(() => {
    const h = new Date().getHours()
    return h < 12 ? N_('Good morning') : h < 18 ? N_('Good afternoon') : N_('Good evening')
  })
  function ago(when) {
    const s = (Date.now() - new Date(when)) / 1000
    if (s < 60) return t('just now')
    if (s < 3600) return t('{n} min ago', { n: Math.floor(s / 60) })
    if (s < 86400) return t('Today {time}', { time: new Date(when).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) })
    return new Date(when).toLocaleDateString([], { day: 'numeric', month: 'short' }) + ' ' + new Date(when).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  }
  const shortName = { smbd: 'SMB', wsdd2: N_('Win discovery'), 'avahi-daemon': 'Bonjour', rsync: 'rsync', vsftpd: 'FTP', ups: 'UPS' }
  const sevDot = { critical: 'bad', warning: 'warn', info: '' }
  const levelText = (v) => v.members.length > 1
    ? t('{level} · {n} disks', { level: v.level.replace('raid', 'RAID '), n: v.members.length })
    : t('{level} · {n} disk', { level: v.level.replace('raid', 'RAID '), n: v.members.length })
  const statusBadge = (s) => ({ healthy: ['good', N_('Healthy')], degraded: ['warn', N_('Degraded')], failed: ['bad', N_('Failed')],
    rebuilding: ['info', N_('Rebuilding')], syncing: ['info', N_('Syncing')], checking: ['info', N_('Checking')], reshaping: ['info', N_('Expanding')] }[s] ?? ['neutral', s])
</script>

<main class="page">
  <div class="page-head">
    <div>
      <div class="eyebrow">{t(greeting)}, {user}</div>
      <h1>{t('Overview')}</h1>
    </div>
    <div class="actions add">
      <div class="segmented-btns" role="group" aria-label={t('Time range')}>
        {#each ['1h', '24h', '7d', '30d'] as r}
          <button class:on={range === r} aria-pressed={range === r} onclick={() => (range = r)}>{r}</button>
        {/each}
      </div>
      <a class="icon-btn bell" href="#/system/events" aria-label={t('Events')} title={t('Events')}>
        <Icon name="bell" size={17} />{#if problems.length}<span class="bell-dot"></span>{/if}
      </a>
    </div>
  </div>

  {#if error}<p class="error">{error}</p>{/if}

  <div class="grid ov-top">
    <section class="card storage">
      <Donut percent={total ? (100 * used) / total : 0} />
      <div class="storage-body">
        <div class="muted small">{t('Storage capacity')}</div>
        <div class="big">{formatBytes(used)} <span class="muted">{t('of {total}', { total: formatBytes(total) })}</span></div>
        <div class="muted small">
          {#if growth}
            {t('{amount} this week', { amount: (growth.perWeek >= 0 ? '+' : '−') + formatBytes(Math.abs(growth.perWeek)) })}{growth.months ? ' · ' + t('full in ~{n} months at this rate', { n: Math.max(1, Math.round(growth.months)) }) : ''}
          {:else}
            {t('Growth appears after a day of history.')}
          {/if}
        </div>
        <div class="pools">
          {#each volumes as v (v.device)}
            {@const [cls, txt] = statusBadge(v.status)}
            <a class="pool" href="#/volumes?sel={encodeURIComponent(v.name)}">
              <span class="dot" class:warn={cls === 'warn'} class:bad={cls === 'bad'}></span>
              <span class="mono">{v.name}</span>
              <span class="muted small hide-sm">{levelText(v)}</span>
              <span class="spacer"></span>
              <span class="small">{formatBytes(v.usedBytes)} / {formatBytes(v.totalBytes || v.sizeBytes)}</span>
              <span class="badge {cls}">{t(txt)}</span>
            </a>
          {:else}
            <a class="pool" href="#/volumes"><span class="muted small">{t('No volume yet: create one')}</span></a>
          {/each}
        </div>
      </div>
    </section>

    {#if top}
      <section class="card attention" class:critical={top.severity === 'critical'}>
        <div class="card-head"><span class="att-icon"><Icon name="alert" size={16} /></span><h2>{t('Needs attention')}</h2></div>
        <h3>{top.subject}</h3>
        <p class="small att-body">{top.body}</p>
        <div class="add att-actions">
          <a class="btn-like" href={reviewLink(top.kind)}>{t('Review')}</a>
          {#if problems.length > 1}<a class="small" href="#/system/events">{t('+{n} more', { n: problems.length - 1 })}</a>{/if}
        </div>
      </section>
    {:else}
      <section class="card all-good">
        <div class="card-head"><span class="ok-icon"><Icon name="check" size={16} /></span><h2>{t('All good')}</h2></div>
        <p class="small muted">{services.some((s) => s.unit === 'ups') ? t('No problem detected: volumes are redundant, disks report healthy, the UPS is on mains power.') : t('No problem detected: volumes are redundant, disks report healthy.')}</p>
      </section>
    {/if}
  </div>

  <div class="grid cols-3 ov-row">
    <section class="card metric">
      <div class="card-head"><span class="muted small">{t('CPU')}</span><span class="spacer"></span>
        <span class="muted small">{sys?.cores === 1 ? t('{n} core', { n: 1 }) : t('{n} cores', { n: sys?.cores ?? '–' })}{cpuTemp ? ` · ${Math.round(cpuTemp)} °C` : ''}</span></div>
      <div class="value">{latest ? Math.round(latest.cpu) : '–'} <small>%</small></div>
      <TimeChart compact height={54} points={metrics} series={[{ key: 'cpu', label: t('CPU'), color: 'var(--series-read)' }]} format={(v) => `${v.toFixed(0)} %`} />
    </section>
    <section class="card metric">
      <div class="card-head"><span class="muted small">{t('Memory')}</span><span class="spacer"></span>
        <span class="muted small">{t('{size} installed', { size: sys ? formatBytes(sys.memTotalBytes) : '' })}</span></div>
      <div class="value">{latest ? formatBytes(latest.memUsed) : '–'} <small>{t('used')}</small></div>
      <TimeChart compact height={54} points={metrics} series={[{ key: 'memUsed', label: t('Memory'), color: 'var(--series-read)' }]} format={formatBytes} />
    </section>
    <section class="card metric">
      <div class="card-head"><span class="muted small">{t('Network')}</span><span class="spacer"></span>
        <span class="muted small">{t('out {rate}', { rate: latest ? fmtRate(latest.netTx) : '–' })}</span></div>
      <div class="value">{latest ? fmtRate(latest.netRx).split(' ')[0] : '–'} <small>{t('{unit} in', { unit: latest ? fmtRate(latest.netRx).split(' ')[1] : '' })}</small></div>
      <TimeChart compact height={54} points={metrics} series={[{ key: 'netRx', label: t('In'), color: 'var(--series-read)' }]} format={fmtRate} />
    </section>
  </div>

  <div class="grid ov-top ov-row">
    <section class="card">
      <div class="card-head">
        <h2>{t('Disk throughput')}</h2>
        <span class="spacer"></span>
        <span class="legend"><span class="key" style="background: var(--series-read)"></span>{t('Read · peak {rate}', { rate: fmtRate(peak('diskRead')) })}</span>
        <span class="legend"><span class="key" style="background: var(--series-write)"></span>{t('Write · peak {rate}', { rate: fmtRate(peak('diskWrite')) })}</span>
      </div>
      <TimeChart height={210} points={metrics} {timeFormat} format={fmtRate}
        series={[{ key: 'diskRead', label: t('Read'), color: 'var(--series-read)' }, { key: 'diskWrite', label: t('Write'), color: 'var(--series-write)' }]} />
    </section>

    <section class="card">
      <div class="card-head"><h2>{t('Drive bays')}</h2><span class="spacer"></span>
        <span class="muted small">{t('{n} of {total} in use', { n: bays.filter((b) => b.disk).length, total: BAYS })}</span></div>
      <div class="bays">
        {#each bays as b (b.n)}
          {@const st = diskState(b.disk)}
          <a class="bay" class:warn={st === 'warn'} class:bad={st === 'bad'} class:empty={!b.disk}
            href={b.disk ? `#/volumes?disk=${b.disk.name}` : '#/volumes'}>
            <span class="dot" class:warn={st === 'warn'} class:bad={st === 'bad'} class:off={st === 'off'} class:empty={!b.disk}></span>
            <span class="bay-n">{b.n}</span>
            {#if b.disk}
              <span class="bay-what">{volumeOf[b.disk.name] ?? t('free')} · {b.disk.model || b.disk.name}{st === 'warn' ? ' · ' + t('SMART warning') : st === 'bad' ? ' · ' + t('failing') : ''}</span>
              <span class="spacer"></span>
              <span class="small">{b.disk.health?.temperatureC ? `${b.disk.health.temperatureC}°` : ''}</span>
            {:else}
              <span class="muted">{t('Empty')}</span>
            {/if}
          </a>
        {/each}
      </div>
    </section>
  </div>

  <div class="grid cols-3 ov-row">
    <section class="card">
      <div class="card-head"><h2>{t('Services')}</h2><a class="small" href="#/system/services">{t('Configure')}</a></div>
      <div class="svc-grid">
        {#each services as s (s.unit)}
          <div class="tile">
            <div class="svc-name"><span class="dot" class:off={!s.active}></span>{shortName[s.unit] ? t(shortName[s.unit]) : s.label}</div>
            <div class="muted small">{s.detail || (s.active ? t('Running') : t('Stopped'))}</div>
          </div>
        {/each}
      </div>
    </section>

    <section class="card">
      <div class="card-head"><h2>{t('Data protection')}</h2><a class="small" href="#/backups">{t('Manage')}</a></div>
      <div class="list">
        {#each backups.slice(0, 4) as b (b.id)}
          <div class="row">
            <span class="prot-icon"><Icon name={b.target.kind === 'usb' ? 'usb' : 'refresh'} size={15} /></span>
            <div class="grow"><div>{b.name}</div><div class="muted small">{b.lastRun.status ? ago(b.lastRun.started) + (b.lastRun.summary ? ' · ' + b.lastRun.summary : '') : t('Never ran')}</div></div>
            <span class="spacer"></span>
            <span class="badge {b.lastRun.status === 'done' ? 'good' : b.lastRun.status === 'failed' ? 'bad' : 'neutral'}">
              {b.lastRun.status === 'done' ? t('OK') : b.lastRun.status === 'failed' ? t('Failed') : '—'}</span>
          </div>
        {/each}
        {#each pools.filter((v) => v.level !== 'raid0') as v (v.device)}
          <div class="row">
            <span class="prot-icon"><Icon name="shield" size={15} /></span>
            <div class="grow"><div>{t('Volume check · {name}', { name: v.name })}</div>
              <div class="muted small">{v.lastSyncAction === 'check' || v.lastSyncAction === 'repair' ? t('{n} inconsistencies', { n: v.mismatchCount }) : t('Not checked since start')}</div></div>
            <span class="spacer"></span>
            <span class="badge {v.lastSyncAction === 'check' && v.mismatchCount === 0 ? 'good' : v.mismatchCount > 0 ? 'warn' : 'neutral'}">
              {v.lastSyncAction === 'check' && v.mismatchCount === 0 ? t('OK') : v.mismatchCount > 0 ? t('Found issues') : '—'}</span>
          </div>
        {/each}
        {#if !backups.length}<p class="muted small">{t('No backup job yet.')} {t("RAID isn't a backup:")} <a href="#/backups">{t('set one up')}</a>.</p>{/if}
      </div>
    </section>

    <section class="card">
      <div class="card-head"><h2>{t('Activity')}</h2><a class="small" href="#/system/events">{t('View all')}</a></div>
      <ol class="activity">
        {#each events.slice(0, 5) as e, i (i)}
          <li><span class="dot {sevDot[e.severity]}" class:info={e.severity === 'info'}></span>
            <div><div class="small">{e.subject}</div><div class="muted small">{ago(e.time)}</div></div></li>
        {:else}
          <li class="muted small">{t('No events yet.')}</li>
        {/each}
      </ol>
    </section>
  </div>
</main>

<style>
  .ov-top { grid-template-columns: minmax(0, 2fr) minmax(0, 1fr); }
  .ov-row { margin-top: 14px; }
  .storage { display: flex; gap: 1.5rem; align-items: center; }
  .storage-body { flex: 1; min-width: 0; }
  .big { font-size: 1.85rem; font-weight: 600; letter-spacing: -0.02em; }
  .big .muted { font-size: 0.9rem; font-weight: 400; }
  .pools { display: flex; flex-direction: column; gap: 6px; margin-top: 0.8rem; }
  .pool { display: flex; align-items: center; gap: 0.6rem; padding: 0.5rem 0.7rem; background: var(--surface-2); border: 1px solid var(--border); border-radius: var(--radius-sm); color: var(--text); text-decoration: none; }
  .pool:hover { border-color: var(--border-strong); text-decoration: none; }
  .attention { border-color: color-mix(in srgb, var(--warn) 40%, transparent); background: linear-gradient(var(--warn-bg), var(--warn-bg)), var(--surface); display: flex; flex-direction: column; }
  .attention.critical { border-color: color-mix(in srgb, var(--bad) 45%, transparent); background: linear-gradient(var(--bad-bg), var(--bad-bg)), var(--surface); }
  .attention h2 { color: var(--warn); }
  .attention.critical h2 { color: var(--bad); }
  .att-icon, .ok-icon { width: 28px; height: 28px; border-radius: 8px; display: grid; place-items: center; background: var(--warn-bg); color: var(--warn); }
  .attention.critical .att-icon { background: var(--bad-bg); color: var(--bad); }
  .ok-icon { background: var(--good-bg); color: var(--good); }
  .att-body { color: var(--text-2); }
  .att-actions { margin-top: auto; padding-top: 0.75rem; }
  .btn-like { padding: 0.45rem 0.95rem; border-radius: var(--radius-sm); background: var(--warn); color: #1a1205; font-weight: 500; text-decoration: none; }
  .attention.critical .btn-like { background: var(--bad); color: #fff; }
  .metric .value { font-size: 1.6rem; font-weight: 600; letter-spacing: -0.02em; margin: -0.3rem 0 0.4rem; font-variant-numeric: tabular-nums; }
  .metric .value small { font-size: 0.82rem; color: var(--muted); font-weight: 500; }
  .metric .card-head { margin-bottom: 0.6rem; }
  .legend { display: inline-flex; align-items: center; gap: 0.35rem; font-size: 0.78rem; color: var(--text-2); }
  .key { width: 10px; height: 10px; border-radius: 3px; }
  .bays { display: flex; flex-direction: column; gap: 6px; }
  .bay { display: flex; align-items: center; gap: 0.6rem; padding: 0.45rem 0.65rem; border-radius: var(--radius-sm); background: var(--surface-2); border: 1px solid var(--border); font-size: 0.85rem; color: var(--text); text-decoration: none; }
  .bay:hover { border-color: var(--border-strong); text-decoration: none; }
  .bay.warn { background: var(--warn-bg); border-color: color-mix(in srgb, var(--warn) 35%, transparent); }
  .bay.bad { background: var(--bad-bg); border-color: color-mix(in srgb, var(--bad) 35%, transparent); }
  .bay.empty { background: none; border-style: dashed; }
  .bay-n { width: 1rem; color: var(--muted); font-variant-numeric: tabular-nums; }
  .bay-what { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .svc-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 8px; }
  .svc-name { display: flex; align-items: center; gap: 0.45rem; font-weight: 500; }
  .prot-icon { width: 28px; height: 28px; border-radius: 8px; display: grid; place-items: center; background: var(--surface-2); border: 1px solid var(--border); color: var(--muted); flex: none; }
  .grow { flex: 1; min-width: 0; }
  .card :global(.list .row) { flex-wrap: nowrap; }
  .activity { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 0.75rem; }
  .activity li { display: flex; gap: 0.65rem; align-items: flex-start; }
  .activity .dot { margin-top: 0.4rem; }
  .activity .dot.info { background: var(--accent); }
  .bell { position: relative; border: 1px solid var(--border); background: var(--surface); width: 34px; height: 34px; }
  .bell-dot { position: absolute; top: 6px; right: 7px; width: 7px; height: 7px; border-radius: 50%; background: var(--warn); }
  @media (max-width: 1100px) { .ov-top { grid-template-columns: 1fr; } }
  @media (max-width: 600px) { .storage { flex-direction: column; align-items: stretch; } .svc-grid { grid-template-columns: 1fr; } }
</style>
