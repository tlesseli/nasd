<script>
  import { onMount } from 'svelte'
  import { api, formatBytes, formatDuration } from '../api.js'
  import TextDialog from './TextDialog.svelte'

  let { onSessionLost } = $props()

  let sys = $state(null)
  let disks = $state([])
  let volumes = $state([])
  let services = $state([])
  let sensors = $state([])
  let events = $state([])
  let smart = $state(null) // {title, text}
  let locating = $state([])
  let error = $state('')

  async function refresh() {
    try {
      ;[sys, disks, volumes, services] = await Promise.all([
        api.get('/api/system'), api.get('/api/disks'), api.get('/api/volumes'), api.get('/api/services'),
      ])
      ;[sensors, events] = await Promise.all([api.get('/api/sensors'), api.get('/api/events')])
      locating = (await api.get('/api/hardware')).locating
      error = ''
    } catch (e) {
      if (e.status === 401) onSessionLost()
      else error = e.message
    }
  }

  onMount(() => {
    refresh()
    const timer = setInterval(refresh, 10_000)
    return () => clearInterval(timer)
  })

  async function showSmart(d) {
    smart = { title: `SMART report: ${d.name} (${d.model})`, text: 'Loading…' }
    try {
      smart.text = (await api.get(`/api/disks/${d.name}/smart`)).report
    } catch (e) {
      smart.text = e.message
    }
  }
  async function locate(d, on) {
    try {
      if (on) await api.post(`/api/disks/${d.name}/locate`, { seconds: 60 })
      else await api.post(`/api/disks/${d.name}/locate/stop`)
      locating = (await api.get('/api/hardware')).locating
    } catch (e) {
      error = e.message
    }
  }

  const sevClass = { critical: 'bad', warning: 'warn', info: 'neutral' }

  // One word for the state of a disk, used for the colored badge.
  function status(d) {
    const h = d.health
    if (!h || !h.supported) return { cls: 'neutral', text: 'No SMART' }
    if (!h.passed) return { cls: 'bad', text: 'Failing' }
    if (h.reallocatedSectors > 0 || h.pendingSectors > 0 || h.uncorrectableSectors > 0)
      return { cls: 'warn', text: 'Warning' }
    return { cls: 'good', text: 'Healthy' }
  }

  let memUsedPct = $derived(sys ? Math.round(100 * (1 - sys.memAvailableBytes / sys.memTotalBytes)) : 0)
  // disk name -> volume name, to show which volume each disk belongs to
  let volumeOf = $derived(Object.fromEntries(volumes.flatMap((v) => v.members.map((m) => [m.disk, v.name]))))
  let dataDisks = $derived(disks.filter((d) => d.role === 'data'))
  let systemDisks = $derived(disks.filter((d) => d.role === 'system'))
</script>

<main class="page">
  {#if error}<p class="error">{error}</p>{/if}

  {#if sys}
    <section class="stats">
      <div class="stat"><span class="label">Uptime</span><span class="value">{formatDuration(sys.uptimeSeconds)}</span></div>
      <div class="stat"><span class="label">Load (1 min)</span><span class="value">{sys.load?.[0]?.toFixed(2)}</span></div>
      <div class="stat">
        <span class="label">Memory</span>
        <span class="value">{memUsedPct}%</span>
        <span class="meter"><span style="width: {memUsedPct}%"></span></span>
        <span class="muted small">{formatBytes(sys.memTotalBytes - sys.memAvailableBytes)} of {formatBytes(sys.memTotalBytes)}</span>
      </div>
      <div class="stat"><span class="label">System</span><span class="value small">{sys.cpu}</span><span class="muted small">Linux {sys.kernel}</span></div>
    </section>
  {/if}

  {#if events.some((e) => e.severity !== 'info')}
    {@const recent = events.filter((e) => e.severity !== 'info').slice(0, 3)}
    <h2>Recent alerts <a class="small" href="#/system">all events</a></h2>
    <div class="card list">
      {#each recent as e, i (i)}
        <div class="row">
          <span class="badge {sevClass[e.severity]}">{e.severity}</span>
          <strong>{e.subject}</strong>
          <span class="spacer"></span>
          <span class="muted small">{new Date(e.time).toLocaleString()}</span>
        </div>
      {/each}
    </div>
  {/if}

  <h2>Temperatures and fans</h2>
  <div class="card list">
    {#each sensors as s (s.id)}
      {@const hot = s.kind === 'temp' && s.limit && s.value >= s.limit}
      {@const stopped = s.kind === 'fan' && s.value === 0}
      <div class="row">
        <span class="badge {hot || stopped ? 'bad' : 'good'}">{s.kind === 'temp' ? `${s.value.toFixed(0)} °C` : `${s.value} RPM`}</span>
        <strong>{s.label}</strong>
        <span class="muted small">{s.chip}{s.limit ? ` · limit ${s.kind === 'temp' ? s.limit + ' °C' : s.limit + ' RPM'}` : ''}</span>
      </div>
    {:else}
      <p class="muted small">No hardware sensors found (normal in a virtual machine; the real ReadyNAS reports its fan and temperatures).</p>
    {/each}
  </div>

  {#if services.length}
    <h2>Services</h2>
    <div class="card list">
      {#each services as svc (svc.unit)}
        <div class="row">
          <span class="badge {svc.active ? 'good' : 'bad'}">{svc.active ? 'Running' : 'Stopped'}</span>
          <strong>{svc.label}</strong>
          <span class="muted small mono">{svc.unit}</span>
        </div>
      {/each}
    </div>
  {/if}

  <h2>Data disks <span class="muted">({dataDisks.length})</span></h2>
  <div class="bays">
    {#each dataDisks as d (d.path)}
      {@const s = status(d)}
      <article class="card disk">
        <div class="disk-head">
          <strong>{d.name}</strong>
          <span class="badge {s.cls}">{s.text}</span>
        </div>
        <div class="disk-size">{formatBytes(d.sizeBytes)}</div>
        <dl>
          <dt>Model</dt><dd>{d.model || '—'}</dd>
          <dt>Serial</dt><dd class="mono">{d.serial || '—'}</dd>
          <dt>In use as</dt><dd>{volumeOf[d.name] ? `volume ${volumeOf[d.name]}` : d.usage || 'free'}</dd>
          {#if d.health?.supported}
            <dt>Temperature</dt><dd>{d.health.temperatureC ? `${d.health.temperatureC} °C` : '—'}</dd>
            <dt>Power-on</dt><dd>{d.health.powerOnHours.toLocaleString()} h</dd>
            <dt>Bad sectors</dt><dd>{d.health.reallocatedSectors + d.health.pendingSectors}</dd>
          {/if}
        </dl>
        <div class="disk-actions">
          {#if d.health?.supported}
            <button class="link small" onclick={() => showSmart(d)}>SMART details</button>
          {/if}
          {#if locating.includes(d.name)}
            <button class="link small" onclick={() => locate(d, false)}>Stop blinking</button>
            <span class="badge info blink">Blinking</span>
          {:else}
            <button class="link small" onclick={() => locate(d, true)} title="Makes the disk's activity light blink for 60 s">Locate</button>
          {/if}
        </div>
      </article>
    {:else}
      <p class="muted">No data disks detected.</p>
    {/each}
  </div>

  {#if systemDisks.length}
    <p class="muted small">System disk: {systemDisks.map((d) => `${d.name} (${formatBytes(d.sizeBytes)})`).join(', ')}</p>
  {/if}
</main>

{#if smart}<TextDialog title={smart.title} text={smart.text} onClose={() => (smart = null)} />{/if}
