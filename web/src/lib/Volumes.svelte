<script>
  import { onMount } from 'svelte'
  import { api, formatBytes, formatDuration } from '../api.js'
  import CreateVolume from './CreateVolume.svelte'
  import JobPanel from './JobPanel.svelte'
  import ConfirmDialog from './ConfirmDialog.svelte'
  import VolumeMaintenance from './VolumeMaintenance.svelte'
  import TextDialog from './TextDialog.svelte'
  import Icon from '../ui/Icon.svelte'

  let { onSessionLost, params = {} } = $props()

  let selected = $state(null) // name of the volume shown in the side panel
  let maint = $state({}) // volume -> maintenance settings (last check)
  let locating = $state([])
  let smart = $state(null)
  let notice = $state('')
  $effect(() => {
    if (params.sel) selected = params.sel
  })

  let volumes = $state([])
  let disks = $state([])
  let loaded = $state(false)
  let error = $state('')
  let job = $state(null) // the background job being shown
  let creating = $state(false)
  let dialog = $state(null) // props for ConfirmDialog, or null
  let addChoice = $state({}) // volume name -> chosen disk

  let adoptName = $state('data')
  async function refresh() {
    try {
      ;[volumes, disks] = await Promise.all([api.get('/api/volumes'), api.get('/api/disks')])
      locating = (await api.get('/api/hardware')).locating
      loaded = true
    } catch (e) {
      if (e.status === 401) onSessionLost()
      else error = e.message
    }
  }

  onMount(() => {
    refresh()
    // Pick up a job started earlier (e.g. before a page reload).
    api.get('/api/jobs').then((jobs) => {
      if (jobs[0]?.status === 'running') job = jobs[0]
    }).catch(() => {})
    // Fast refresh: rebuild progress moves quickly.
    const timer = setInterval(refresh, 3000)
    loadMaint()
    const slow = setInterval(loadMaint, 30_000)
    return () => (clearInterval(timer), clearInterval(slow))
  })

  async function loadMaint() {
    const list = await api.get('/api/volumes').catch(() => [])
    const entries = await Promise.all(list.map(async (v) => [v.name, await api.get(`/api/volumes/${v.name}/maintenance`).catch(() => ({}))]))
    maint = Object.fromEntries(entries)
  }

  async function diskAction(d, what) {
    error = notice = ''
    try {
      if (what === 'locate') await api.post(`/api/disks/${d.name}/locate`, { seconds: 60 })
      if (what === 'unlocate') await api.post(`/api/disks/${d.name}/locate/stop`)
      if (what === 'selftest') {
        await api.post(`/api/disks/${d.name}/selftest`)
        notice = `SMART short test started on ${d.name}: about 2 minutes, run by the disk itself. The result shows in its SMART report.`
      }
      if (what === 'smart') {
        smart = { title: `SMART report: ${d.name} (${d.model})`, text: 'Loading…' }
        smart.text = (await api.get(`/api/disks/${d.name}/smart`)).report
      }
      locating = (await api.get('/api/hardware')).locating
    } catch (e) {
      error = e.message
    }
  }

  // Every action goes through here: start the job, show its log. Errors are
  // thrown back to the form / dialog that started it.
  async function startJob(path, body) {
    try {
      job = await api.post(path, body)
    } catch (e) {
      if (e.status === 401) onSessionLost()
      throw e
    }
    creating = false
    dialog = null
    refresh()
  }

  let memberOf = $derived(Object.fromEntries(volumes.flatMap((v) => v.members.map((m) => [m.disk, v.name]))))
  let dataDisks = $derived(disks.filter((d) => d.role === 'data' && !memberOf[d.name]))
  let freeDisks = $derived(dataDisks.filter((d) => !d.usage && !d.partitions?.length))
  let dirtyDisks = $derived(dataDisks.filter((d) => d.usage || d.partitions?.length))
  let busy = $derived(job?.status === 'running')

  const statusClass = {
    healthy: 'good', syncing: 'info', rebuilding: 'info', checking: 'info', reshaping: 'info',
    degraded: 'warn', failed: 'bad', inactive: 'bad',
  }
  const syncLabel = { resync: 'Initial sync', recover: 'Rebuilding', check: 'Checking', repair: 'Repairing', reshape: 'Reshaping' }
  const levelLabel = (l) => l?.replace('raid', 'RAID ') || 'unknown'

  // A spare with a slot is being rebuilt into a missing position, so it is
  // not "missing". (A brand-new RAID5 is built that way by mdadm.)
  const rebuilding = (v) => v.members.filter((m) => m.state.includes('spare') && m.slot >= 0).length
  const missing = (v) => Math.max(0, v.degraded - rebuilding(v))

  function memberLabel(m) {
    if (m.state.includes('faulty')) return { cls: 'bad', text: 'Failed' }
    if (m.state.includes('spare')) return { cls: 'neutral', text: m.slot >= 0 ? 'Rebuilding' : 'Spare' }
    if (m.state.includes('in_sync')) return { cls: 'good', text: 'OK' }
    return { cls: 'neutral', text: m.state }
  }

  function askDelete(v) {
    dialog = {
      title: `Delete volume ${v.name}?`,
      message: `This unmounts ${v.mountpoint || 'the volume'}, destroys the RAID and erases ${v.members.map((m) => m.disk).join(', ')}. All files on it are lost.`,
      expected: v.name,
      confirmLabel: 'Delete volume',
      onConfirm: () => startJob(`/api/volumes/${encodeURIComponent(v.name)}/delete`, { confirm: v.name }),
    }
  }

  function askRemove(v, m) {
    const failed = m.state.includes('faulty')
    dialog = {
      title: `Remove ${m.disk} from ${v.name}?`,
      message: failed
        ? `${m.disk} has failed. Removing it lets you replace it with a new disk.`
        : m.state.includes('spare')
          ? `${m.disk} is a spare; the volume won't be affected.`
          : `${m.disk} will be marked as failed and taken out. ${v.name} keeps working but becomes degraded until you add a replacement disk.`,
      confirmLabel: 'Remove disk',
      onConfirm: () => startJob(`/api/volumes/${encodeURIComponent(v.name)}/remove`, { disk: m.disk }),
    }
  }

  function askErase(d) {
    dialog = {
      title: `Erase ${d.name}?`,
      message: `Removes every partition, RAID signature and filesystem from ${d.name} (${d.model}, ${formatBytes(d.sizeBytes)}). Anything on it is lost.`,
      expected: d.name,
      confirmLabel: 'Erase disk',
      onConfirm: () => startJob(`/api/disks/${d.name}/erase`, { confirm: d.name }),
    }
  }

  // Run an action started from a button (not a dialog): show its error on the page.
  async function run(action) {
    error = ''
    try {
      await action()
    } catch (e) {
      error = e.message
    }
  }

  // Spares that are idle (not currently rebuilding into a slot).
  const spares = (v) => v.members.filter((m) => m.state.includes('spare') && m.slot < 0)
  // Disks that can be added to / used to expand a volume: free disks, plus its idle spares.
  const candidates = (v) => [
    ...spares(v).map((m) => ({ name: m.disk, label: `${m.disk} · current hot spare` })),
    ...freeDisks.map((d) => ({ name: d.name, label: `${d.name} · ${formatBytes(d.sizeBytes)} · free` })),
  ]
  const parity = { raid5: 1, raid6: 2 }
  const canExpand = (v) => v.level in parity && v.status === 'healthy'

  function askExpand(v, disk) {
    const p = parity[v.level]
    const n = v.raidDisks
    const newSize = (v.sizeBytes / (n - p)) * (n + 1 - p)
    dialog = {
      title: `Expand ${v.name} with ${disk}?`,
      message: `${v.name} goes from ${n} to ${n + 1} disks and from ${formatBytes(v.sizeBytes)} to about ${formatBytes(newSize)}. ` +
        `The data is reshaped across all disks: this takes a long time (hours with large disks). ` +
        `The volume stays usable and protected meanwhile, but avoid restarting the NAS. Other disk operations wait until it's done.`,
      confirmLabel: 'Expand volume',
      danger: false,
      onConfirm: () => startJob(`/api/volumes/${v.name}/expand`, { disk }),
    }
  }

  const sel = $derived(volumes.find((v) => v.name === selected))
  const tolerates = { raid0: 0, raid1: (n) => n - 1, raid5: 1, raid6: 2, raid10: 1 }
  // The share of each disk the array uses: a mirror keeps everything on every
  // disk; striped levels spread the size over their data disks.
  const perDisk = (v) => {
    const n = Math.max(1, v.raidDisks)
    const data = { raid1: 1, raid10: n / 2, raid5: n - 1, raid6: n - 2 }[v.level] ?? n
    return v.sizeBytes / Math.max(1, data)
  }
  const survives = (v) => {
    const t = tolerates[v.level]
    const n = typeof t === 'function' ? t(v.raidDisks) : t ?? 0
    return n === 0 ? 'none' : `${n} disk${n > 1 ? 's' : ''}`
  }
  const SEGMENTS = 24
  const lastCheck = (v) => {
    const m = maint[v.name]
    if (m?.lastCheck) return new Date(m.lastCheck).toLocaleDateString([], { day: 'numeric', month: 'short' })
    return v.level === 'raid0' ? 'n/a' : 'never'
  }
  const physical = $derived([...disks].filter((d) => d.role !== 'usb')
    .sort((a, b) => (a.bay || 99) - (b.bay || 99) || a.name.localeCompare(b.name)))
  function health(d) {
    const h = d.health
    if (d.role === 'system') return { cls: 'neutral', text: 'System disk' }
    if (h?.standby) return { cls: 'neutral', text: 'Asleep' }
    if (!h || !h.supported) return { cls: 'neutral', text: 'No SMART' }
    if (!h.passed) return { cls: 'bad', text: 'Failing' }
    const bad = h.reallocatedSectors + h.pendingSectors + h.uncorrectableSectors
    if (bad > 0) return { cls: 'warn', text: `${bad} bad sectors` }
    return { cls: 'good', text: 'Passed' }
  }
</script>

<main class="page">
  <div class="page-head">
    <div>
      <div class="eyebrow">{volumes.length} volume{volumes.length === 1 ? '' : 's'} · {physical.filter((d) => d.role === 'data').length} data disks</div>
      <h1>Volumes &amp; Disks</h1>
    </div>
    <div class="actions">
      {#if !creating}
        <button onclick={() => (creating = true)} disabled={busy || !freeDisks.length}
          title={freeDisks.length ? '' : 'No free disks'}>Create volume</button>
      {/if}
    </div>
  </div>

  {#if error}<p class="error">{error}</p>{/if}
  {#if notice}<p class="notice">{notice}</p>{/if}
  {#if job}<JobPanel {job} onFinished={refresh} onClose={() => (job = null)} />{/if}
  {#if creating}
    <CreateVolume {freeDisks} onCreate={(req) => startJob('/api/volumes', req)} onCancel={() => (creating = false)} />
  {/if}

  <div class:with-panel={!!sel}>
    <div class="stack">
      <div class="grid cols-2 pools">
        {#each volumes as v (v.device)}
          {@const pct = v.totalBytes ? (100 * v.usedBytes) / v.totalBytes : 0}
          <button class="card pool-card" class:selected={selected === v.name} onclick={() => (selected = selected === v.name ? null : v.name)}
            aria-pressed={selected === v.name}>
            <div class="volume-head">
              <span class="dot" class:warn={statusClass[v.status] === 'warn'} class:bad={statusClass[v.status] === 'bad'}></span>
              <span class="mono pool-name">{v.name}</span>
              <span class="spacer"></span>
              {#if !v.managed && !v.mountpoint}<span class="badge neutral">Found</span>{/if}
              <span class="badge {statusClass[v.status] ?? 'neutral'}">{v.status[0].toUpperCase() + v.status.slice(1)}</span>
            </div>
            <div class="muted small">{levelLabel(v.level)} · {v.raidDisks} × {formatBytes(perDisk(v))} · {v.filesystem || 'no filesystem'}</div>
            <div class="pool-usage">
              {#if v.mountpoint}
                <span class="big">{formatBytes(v.usedBytes)} <span class="muted small">used</span></span>
                <span class="muted small">{formatBytes(v.availBytes)} free · {formatBytes(v.totalBytes || v.sizeBytes)}</span>
              {:else}
                <span class="big">{formatBytes(v.sizeBytes)}</span>
                <span class="muted small">not mounted</span>
              {/if}
            </div>
            <div class="segbar" aria-label="{Math.round(pct)}% used">
              {#each Array(SEGMENTS) as _, i}<span class:on={i < Math.round((pct / 100) * SEGMENTS)} class:warn={pct >= 90}></span>{/each}
            </div>
            {#if v.sync}
              <div class="small">{syncLabel[v.sync.action] ?? v.sync.action} {v.sync.percent.toFixed(1)}%{v.sync.etaSeconds ? ` · ${formatDuration(v.sync.etaSeconds)} left` : ''}</div>
            {/if}
            <div class="grid cols-4 tiles">
              <div class="tile"><div class="label">Survives</div><div class="value">{survives(v)}</div></div>
              <div class="tile"><div class="label">Filesystem</div><div class="value">{v.filesystem || '—'}</div></div>
              <div class="tile"><div class="label">Quotas</div><div class="value">{v.quotas ? 'on' : 'off'}</div></div>
              <div class="tile"><div class="label">Last check</div><div class="value">{lastCheck(v)}</div></div>
            </div>
          </button>
        {:else}
          {#if loaded && !creating}
            <div class="card empty span-2">
              <p><strong>No volumes yet.</strong></p>
              <p class="muted">A volume combines disks into one RAID-protected space. Create one to start storing files.</p>
            </div>
          {/if}
        {/each}
      </div>

      <section class="card table-card">
        <div class="card-head"><h2>Physical disks</h2></div>
        <table class="data">
          <thead><tr><th>Slot</th><th>Model</th><th class="opt">Serial</th><th>Size</th><th>Volume</th><th>Temp</th><th class="opt">Power-on</th><th>Health</th><th></th></tr></thead>
          <tbody>
            {#each physical as d (d.name)}
              {@const h = health(d)}
              {@const dirty = d.role === 'data' && !memberOf[d.name] && (d.usage || d.partitions?.length)}
              <tr class:highlight={h.cls === 'warn' || h.cls === 'bad' || params.disk === d.name}>
                <td><span class="dot" class:warn={h.cls === 'warn'} class:bad={h.cls === 'bad'} class:off={h.cls === 'neutral'}></span>
                  {d.role === 'system' ? 'System' : d.bay ? `Bay ${d.bay}` : d.name} <span class="muted small">{d.name}</span></td>
                <td>{d.model || '—'}</td>
                <td class="mono small opt">{d.serial || '—'}</td>
                <td>{formatBytes(d.sizeBytes)}</td>
                <td class="mono">{memberOf[d.name] ?? (dirty ? 'old data' : d.role === 'data' ? 'free' : '—')}</td>
                <td>{d.health?.temperatureC ? `${d.health.temperatureC} °C` : '—'}</td>
                <td class="opt">{d.health?.powerOnHours ? `${d.health.powerOnHours.toLocaleString()} h` : '—'}</td>
                <td><span class="badge {h.cls}">{h.text}</span>
                  {#if locating.includes(d.name)}<span class="badge info blink">Blinking</span>{/if}</td>
                <td class="right row-actions">
                  {#if d.role === 'data'}
                    {#if locating.includes(d.name)}
                      <button class="link" onclick={() => diskAction(d, 'unlocate')}>Stop</button>
                    {:else}
                      <button class="link" onclick={() => diskAction(d, 'locate')} title="Blink the bay light for 60 s">Locate</button>
                    {/if}
                  {/if}
                  {#if d.health?.supported}
                    <button class="link" onclick={() => diskAction(d, 'smart')}>SMART</button>
                    <button class="link" onclick={() => diskAction(d, 'selftest')} title="SMART short self-test (2 min)">Test</button>
                  {/if}
                  {#if dirty}<button class="link danger-text" onclick={() => askErase(d)} disabled={busy}>Erase</button>{/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
        {#if dirtyDisks.length}
          <p class="hint table-note">“Old data”: partitions or RAID signatures from a previous life (e.g. the original ReadyNAS OS). Erase them to use the disk.</p>
        {/if}
      </section>
    </div>

    {#if sel}
      {@const v = sel}
      <aside class="card panel" aria-label="Volume {v.name}">
        <div class="panel-head">
          <div><h2 class="mono">{v.name}</h2><div class="muted small">{levelLabel(v.level)} · {v.device}{v.mountpoint ? ` · ${v.mountpoint}` : ''}</div></div>
          <span class="spacer"></span>
          <button class="icon-btn" onclick={() => (selected = null)} aria-label="Close"><Icon name="x" size={16} /></button>
        </div>

        {#if v.sync}
          <div class="sync">
            <span>{syncLabel[v.sync.action] ?? v.sync.action}: <strong>{v.sync.percent.toFixed(1)}%</strong>
              {#if v.sync.speedKBs}<span class="muted">· {formatBytes(v.sync.speedKBs * 1024)}/s · about {formatDuration(v.sync.etaSeconds)} left</span>{/if}
            </span>
            <span class="meter"><span style="width: {v.sync.percent}%"></span></span>
            {#if v.sync.action === 'resync'}<span class="muted small">The volume is usable during the initial sync, just slower.</span>{/if}
          </div>
        {/if}
        {#if v.status === 'degraded' && missing(v) > 0}
          <p class="warn-box">A disk is missing: the volume has no redundancy left. Add a disk to start the rebuild.</p>
        {/if}
        {#if !v.managed && !v.mountpoint}
          {#if v.filesystem === 'ext4' || v.filesystem === 'btrfs'}
            <div class="notice-box">
              <p><strong>Found on the disks.</strong> This volume was made by another system (e.g. the original ReadyNAS OS){v.lv ? `, with LVM (${v.lv})` : ''}. Use it as is: its files stay where they are.</p>
              <p class="muted small">The array gets the name below, its {v.filesystem} filesystem is checked, then mounted. Then create shares on it, one per existing folder you want to share.</p>
              <div class="add">
                <label>Name <input bind:value={adoptName} placeholder="data" maxlength="16" /></label>
                <button onclick={() => run(() => startJob(`/api/volumes/${encodeURIComponent(v.name)}/adopt`, { name: adoptName }))} disabled={busy || !adoptName}>Use this volume</button>
              </div>
            </div>
          {:else}
            <p class="hint">Made by another system ({v.filesystem || 'no filesystem'}): nothing to mount. Delete it to reuse its disk space.</p>
          {/if}
        {/if}
        {#if v.canGrowFS}
          <div class="warn-box">
            <p>The filesystem uses only {formatBytes(v.totalBytes)} of the {formatBytes(v.sizeBytes)} volume.</p>
            <button class="secondary" onclick={() => run(() => startJob(`/api/volumes/${encodeURIComponent(v.name)}/grow-fs`, {}))} disabled={busy}>Grow filesystem</button>
          </div>
        {/if}

        <div>
          <h3>Disks</h3>
          <table class="members">
            <tbody>
              {#each v.members as m (m.device)}
                {@const l = memberLabel(m)}
                {@const d = disks.find((x) => x.name === m.disk)}
                <tr>
                  <td><strong>{d?.bay ? `Bay ${d.bay}` : m.disk}</strong> <span class="muted small">{m.device}</span></td>
                  <td><span class="badge {l.cls}">{l.text}</span></td>
                  <td class="right">{#if v.level !== 'raid0'}<button class="link danger-text" onclick={() => askRemove(v, m)} disabled={busy}>Remove</button>{/if}</td>
                </tr>
              {/each}
              {#each Array(missing(v)) as _}<tr><td colspan="3" class="error small">Missing disk</td></tr>{/each}
            </tbody>
          </table>
        </div>

        {#if v.level !== 'raid0' && candidates(v).length}
          {@const choice = addChoice[v.name] ?? candidates(v)[0].name}
          {@const isSpare = spares(v).some((m) => m.disk === choice)}
          <div class="stack add-disk">
            <h3>Add a disk</h3>
            <select bind:value={addChoice[v.name]} aria-label="Disk to add">
              {#each candidates(v) as c (c.name)}<option value={c.name}>{c.label}</option>{/each}
            </select>
            <div class="add">
              {#if v.degraded > 0}
                {#if !isSpare}<button class="secondary" onclick={() => run(() => startJob(`/api/volumes/${v.name}/add`, { disk: choice }))} disabled={busy}>Add &amp; rebuild</button>{/if}
              {:else}
                {#if canExpand(v)}<button onclick={() => askExpand(v, choice)} disabled={busy}>Expand volume</button>{/if}
                {#if !isSpare}<button class="secondary" onclick={() => run(() => startJob(`/api/volumes/${v.name}/add`, { disk: choice }))} disabled={busy}>Add as hot spare</button>{/if}
              {/if}
            </div>
            <p class="hint">{v.degraded > 0 ? 'Replaces the missing disk.' : canExpand(v) ? 'Expand adds the disk\'s space. A hot spare stays idle and takes over when a disk fails.' : 'A hot spare stays idle and takes over when a disk fails.'}</p>
          </div>
        {/if}

        <VolumeMaintenance volume={v} {busy} onJob={(j) => (job = j)} />

        <div class="actions">
          <button class="secondary danger-text" onclick={() => askDelete(v)} disabled={busy}>Delete volume</button>
        </div>
      </aside>
    {/if}
  </div>
</main>

{#if dialog}<ConfirmDialog {...dialog} onCancel={() => (dialog = null)} />{/if}
{#if smart}<TextDialog title={smart.title} text={smart.text} onClose={() => (smart = null)} />{/if}

<style>
  .pools { align-items: stretch; }
  .pool-card { text-align: left; color: var(--text); font-weight: 400; display: flex; flex-direction: column; gap: 0.6rem; cursor: pointer; background: var(--surface); }
  .pool-card:hover:not(:disabled) { filter: none; border-color: var(--border-strong); }
  .pool-card.selected { border-color: var(--accent); box-shadow: 0 0 0 1px var(--accent); }
  .pool-name { font-size: 1rem; font-weight: 600; }
  .pool-usage { display: flex; align-items: baseline; justify-content: space-between; gap: 0.5rem; flex-wrap: wrap; margin-top: 0.3rem; }
  .big { font-size: 1.5rem; font-weight: 600; letter-spacing: -0.02em; }
  .tiles { margin-top: 0.3rem; gap: 8px; }
  .table-card :global(td) { white-space: nowrap; }
  /* With the side panel open the table is narrower: drop the least useful columns. */
  .with-panel :global(.opt) { display: none; }
  @media (max-width: 860px) { :global(.opt) { display: none; } }
  .tiles :global(.label) { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .table-note { padding: 0.75rem 1.25rem 1rem; }
  .add-disk { gap: 0.5rem; }
</style>
