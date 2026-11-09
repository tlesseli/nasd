<script>
  import { onMount } from 'svelte'
  import { api, formatBytes, formatDuration } from '../api.js'
  import CreateVolume from './CreateVolume.svelte'
  import JobPanel from './JobPanel.svelte'
  import ConfirmDialog from './ConfirmDialog.svelte'
  import VolumeMaintenance from './VolumeMaintenance.svelte'
  import TextDialog from './TextDialog.svelte'
  import Icon from '../ui/Icon.svelte'
  import { t, N_ } from '../i18n.svelte.js'

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
        notice = t('SMART short test started on {disk}: about 2 minutes, run by the disk itself. The result shows in its SMART report.', { disk: d.name })
      }
      if (what === 'smart') {
        smart = { title: t('SMART report: {disk} ({model})', { disk: d.name, model: d.model }), text: t('Loading…') }
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

  // System and swap arrays live on the data disks too (RAIDiator's layout):
  // they get a line of their own, not a volume card.
  let pools = $derived(volumes.filter((v) => !v.system))
  let systemArrays = $derived(volumes.filter((v) => v.system))
  // A disk shows its data volume; one carrying only the system shows "system".
  let memberOf = $derived(Object.fromEntries([...systemArrays, ...pools].flatMap((v) => v.members.map((m) => [m.disk, v.system ? 'system' : v.name]))))
  let dataDisks = $derived(disks.filter((d) => d.role === 'data' && !memberOf[d.name]))
  let freeDisks = $derived(dataDisks.filter((d) => !d.usage && !d.partitions?.length))
  let dirtyDisks = $derived(dataDisks.filter((d) => d.usage || d.partitions?.length))
  let busy = $derived(job?.status === 'running')

  const statusClass = {
    healthy: 'good', syncing: 'info', rebuilding: 'info', checking: 'info', reshaping: 'info',
    degraded: 'warn', failed: 'bad', inactive: 'bad',
  }
  const syncLabel = { resync: N_('Initial sync'), recover: N_('Rebuilding'), check: N_('Checking'), repair: N_('Repairing'), reshape: N_('Reshaping') }
  const syncText = (a) => (syncLabel[a] ? t(syncLabel[a]) : a)
  const statusLabel = {
    healthy: N_('Healthy'), syncing: N_('Syncing'), rebuilding: N_('Rebuilding'), checking: N_('Checking'), reshaping: N_('Reshaping'),
    degraded: N_('Degraded'), failed: N_('Failed'), inactive: N_('Inactive'),
  }
  const levelLabel = (l) => l?.replace('raid', 'RAID ') || t('unknown')

  // A spare with a slot is being rebuilt into a missing position, so it is
  // not "missing". (A brand-new RAID5 is built that way by mdadm.)
  const rebuilding = (v) => v.members.filter((m) => m.state.includes('spare') && m.slot >= 0).length
  const missing = (v) => Math.max(0, v.degraded - rebuilding(v))

  function memberLabel(m) {
    if (m.state.includes('faulty')) return { cls: 'bad', text: t('Failed') }
    if (m.state.includes('spare')) return { cls: 'neutral', text: m.slot >= 0 ? t('Rebuilding') : t('Spare') }
    if (m.state.includes('in_sync')) return { cls: 'good', text: t('OK') }
    return { cls: 'neutral', text: m.state }
  }

  function askDelete(v) {
    dialog = {
      title: t('Delete volume {name}?', { name: v.name }),
      message: t('This unmounts {path}, destroys the RAID and erases {disks}. All files on it are lost.', { path: v.mountpoint || t('the volume'), disks: v.members.map((m) => m.disk).join(', ') }),
      expected: v.name,
      confirmLabel: t('Delete volume'),
      onConfirm: () => startJob(`/api/volumes/${encodeURIComponent(v.name)}/delete`, { confirm: v.name }),
    }
  }

  function askRemove(v, m) {
    const failed = m.state.includes('faulty')
    dialog = {
      title: t('Remove {disk} from {name}?', { disk: m.disk, name: v.name }),
      message: failed
        ? t('{disk} has failed. Removing it lets you replace it with a new disk.', { disk: m.disk })
        : m.state.includes('spare')
          ? t("{disk} is a spare; the volume won't be affected.", { disk: m.disk })
          : t('{disk} will be marked as failed and taken out. {name} keeps working but becomes degraded until you add a replacement disk.', { disk: m.disk, name: v.name }),
      confirmLabel: t('Remove disk'),
      onConfirm: () => startJob(`/api/volumes/${encodeURIComponent(v.name)}/remove`, { disk: m.disk }),
    }
  }

  function askErase(d) {
    dialog = {
      title: t('Erase {disk}?', { disk: d.name }),
      message: t('Removes every partition, RAID signature and filesystem from {disk} ({model}, {size}). Anything on it is lost.', { disk: d.name, model: d.model, size: formatBytes(d.sizeBytes) }),
      expected: d.name,
      confirmLabel: t('Erase disk'),
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
    ...spares(v).map((m) => ({ name: m.disk, label: t('{disk} · current hot spare', { disk: m.disk }) })),
    ...freeDisks.map((d) => ({ name: d.name, label: t('{disk} · {size} · free', { disk: d.name, size: formatBytes(d.sizeBytes) }) })),
  ]
  const parity = { raid5: 1, raid6: 2 }
  const canExpand = (v) => v.level in parity && v.status === 'healthy'

  function askExpand(v, disk) {
    const p = parity[v.level]
    const n = v.raidDisks
    const newSize = (v.sizeBytes / (n - p)) * (n + 1 - p)
    dialog = {
      title: t('Expand {name} with {disk}?', { name: v.name, disk }),
      message: t('{name} goes from {from} to {to} disks and from {oldsize} to about {newsize}.', { name: v.name, from: n, to: n + 1, oldsize: formatBytes(v.sizeBytes), newsize: formatBytes(newSize) }) + ' ' +
        t('The data is reshaped across all disks: this takes a long time (hours with large disks).') + ' ' +
        t("The volume stays usable and protected meanwhile, but avoid restarting the NAS. Other disk operations wait until it's done."),
      confirmLabel: t('Expand volume'),
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
    const tol = tolerates[v.level]
    const n = typeof tol === 'function' ? tol(v.raidDisks) : tol ?? 0
    return n === 0 ? t('none') : n > 1 ? t('{n} disks', { n }) : t('{n} disk', { n })
  }
  const SEGMENTS = 24
  const lastCheck = (v) => {
    const m = maint[v.name]
    if (m?.lastCheck) return new Date(m.lastCheck).toLocaleDateString([], { day: 'numeric', month: 'short' })
    return v.level === 'raid0' ? t('n/a') : t('never')
  }
  const physical = $derived([...disks].filter((d) => d.role !== 'usb')
    .sort((a, b) => (a.bay || 99) - (b.bay || 99) || a.name.localeCompare(b.name)))
  function health(d) {
    const h = d.health
    if (d.role === 'system') return { cls: 'neutral', text: t('System disk') }
    if (h?.standby) return { cls: 'neutral', text: t('Asleep') }
    if (!h || !h.supported) return { cls: 'neutral', text: t('No SMART') }
    if (!h.passed) return { cls: 'bad', text: t('Failing') }
    const bad = h.reallocatedSectors + h.pendingSectors + h.uncorrectableSectors
    if (bad > 0) return { cls: 'warn', text: t('{n} bad sectors', { n: bad }) }
    return { cls: 'good', text: t('Passed') }
  }
</script>

<main class="page">
  <div class="page-head">
    <div>
      <div class="eyebrow">{pools.length === 1 ? t('{n} volume', { n: pools.length }) : t('{n} volumes', { n: pools.length })} · {t('{n} data disks', { n: physical.filter((d) => d.role === 'data').length })}</div>
      <h1>{t('Volumes & Disks')}</h1>
    </div>
    <div class="actions">
      {#if !creating}
        <button onclick={() => (creating = true)} disabled={busy || !freeDisks.length}
          title={freeDisks.length ? '' : t('No free disks')}>{t('Create volume')}</button>
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
        {#each pools as v (v.device)}
          {@const pct = v.totalBytes ? (100 * v.usedBytes) / v.totalBytes : 0}
          <button class="card pool-card" class:selected={selected === v.name} onclick={() => (selected = selected === v.name ? null : v.name)}
            aria-pressed={selected === v.name}>
            <div class="volume-head">
              <span class="dot" class:warn={statusClass[v.status] === 'warn'} class:bad={statusClass[v.status] === 'bad'}></span>
              <span class="mono pool-name">{v.name}</span>
              <span class="spacer"></span>
              {#if !v.managed && !v.mountpoint}<span class="badge neutral">{t('Found')}</span>{/if}
              <span class="badge {statusClass[v.status] ?? 'neutral'}">{statusLabel[v.status] ? t(statusLabel[v.status]) : v.status[0].toUpperCase() + v.status.slice(1)}</span>
            </div>
            <div class="muted small">{levelLabel(v.level)} · {v.raidDisks} × {formatBytes(perDisk(v))} · {v.filesystem || t('no filesystem')}</div>
            <div class="pool-usage">
              {#if v.mountpoint}
                <span class="big">{formatBytes(v.usedBytes)} <span class="muted small">{t('used')}</span></span>
                <span class="muted small">{t('{size} free', { size: formatBytes(v.availBytes) })} · {formatBytes(v.totalBytes || v.sizeBytes)}</span>
              {:else}
                <span class="big">{formatBytes(v.sizeBytes)}</span>
                <span class="muted small">{t('not mounted')}</span>
              {/if}
            </div>
            <div class="segbar" aria-label={t('{pct}% used', { pct: Math.round(pct) })}>
              {#each Array(SEGMENTS) as _, i}<span class:on={i < Math.round((pct / 100) * SEGMENTS)} class:warn={pct >= 90}></span>{/each}
            </div>
            {#if v.sync}
              <div class="small">{syncText(v.sync.action)} {v.sync.percent.toFixed(1)}%{v.sync.etaSeconds ? ' · ' + t('{time} left', { time: formatDuration(v.sync.etaSeconds) }) : ''}</div>
            {/if}
            <div class="grid cols-4 tiles">
              <div class="tile"><div class="label">{t('Survives')}</div><div class="value">{survives(v)}</div></div>
              <div class="tile"><div class="label">{t('Filesystem')}</div><div class="value">{v.filesystem || '—'}</div></div>
              <div class="tile"><div class="label">{t('Quotas')}</div><div class="value">{v.quotas ? t('on') : t('off')}</div></div>
              <div class="tile"><div class="label">{t('Last check')}</div><div class="value">{lastCheck(v)}</div></div>
            </div>
          </button>
        {:else}
          {#if loaded && !creating}
            <div class="card empty span-2">
              <p><strong>{t('No volumes yet.')}</strong></p>
              <p class="muted">{t('A volume combines disks into one RAID-protected space. Create one to start storing files.')}</p>
            </div>
          {/if}
        {/each}
      </div>
      {#if systemArrays.length}
        <div class="card system-line">
          <Icon name="server" size={16} />
          <div>
            <strong>{t('System')}</strong>
            <span class="muted small">· {t('on every data disk, managed with the volumes')}</span>
            <div class="small">
              {#each systemArrays as v, i (v.device)}{i ? ' · ' : ''}{v.system === 'root' ? t('Operating system') : t('Swap')}: {levelLabel(v.level)}, {v.level === 'raid1' ? t('{n} copies', { n: v.members.length }) : t('{n} disks', { n: v.members.length })} —
                <span class="badge {statusClass[v.status] ?? 'neutral'}">{v.status}</span>{#if v.sync} {syncText(v.sync.action)} {v.sync.percent.toFixed(0)}%{/if}{/each}
            </div>
          </div>
        </div>
      {/if}

      <section class="card table-card">
        <div class="card-head"><h2>{t('Physical disks')}</h2></div>
        <table class="data">
          <thead><tr><th>{t('Slot')}</th><th>{t('Model')}</th><th class="opt">{t('Serial')}</th><th>{t('Size')}</th><th>{t('Volume')}</th><th>{t('Temp')}</th><th class="opt">{t('Power-on')}</th><th>{t('Health')}</th><th></th></tr></thead>
          <tbody>
            {#each physical as d (d.name)}
              {@const h = health(d)}
              {@const dirty = d.role === 'data' && !memberOf[d.name] && (d.usage || d.partitions?.length)}
              <tr class:highlight={h.cls === 'warn' || h.cls === 'bad' || params.disk === d.name}>
                <td><span class="dot" class:warn={h.cls === 'warn'} class:bad={h.cls === 'bad'} class:off={h.cls === 'neutral'}></span>
                  {d.role === 'system' ? t('System') : d.bay ? t('Bay {n}', { n: d.bay }) : d.name} <span class="muted small">{d.name}</span></td>
                <td>{d.model || '—'}</td>
                <td class="mono small opt">{d.serial || '—'}</td>
                <td>{formatBytes(d.sizeBytes)}</td>
                <td class="mono">{memberOf[d.name] ?? (dirty ? t('old data') : d.role === 'data' ? t('free') : '—')}</td>
                <td>{d.health?.temperatureC ? `${d.health.temperatureC} °C` : '—'}</td>
                <td class="opt">{d.health?.powerOnHours ? `${d.health.powerOnHours.toLocaleString()} h` : '—'}</td>
                <td><span class="badge {h.cls}">{h.text}</span>
                  {#if locating.includes(d.name)}<span class="badge info blink">{t('Blinking')}</span>{/if}</td>
                <td class="right row-actions">
                  {#if d.role === 'data'}
                    {#if locating.includes(d.name)}
                      <button class="link" onclick={() => diskAction(d, 'unlocate')}>{t('Stop')}</button>
                    {:else}
                      <button class="link" onclick={() => diskAction(d, 'locate')} title={t('Blink the bay light for 60 s')}>{t('Locate')}</button>
                    {/if}
                  {/if}
                  {#if d.health?.supported}
                    <button class="link" onclick={() => diskAction(d, 'smart')}>SMART</button>
                    <button class="link" onclick={() => diskAction(d, 'selftest')} title={t('SMART short self-test (2 min)')}>{t('Test')}</button>
                  {/if}
                  {#if dirty}<button class="link danger-text" onclick={() => askErase(d)} disabled={busy}>{t('Erase')}</button>{/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
        {#if dirtyDisks.length}
          <p class="hint table-note">{t('“Old data”: partitions or RAID signatures from a previous life (e.g. the original ReadyNAS OS). Erase them to use the disk.')}</p>
        {/if}
      </section>
    </div>

    {#if sel}
      {@const v = sel}
      <aside class="card panel" aria-label={t('Volume {name}', { name: v.name })}>
        <div class="panel-head">
          <div><h2 class="mono">{v.name}</h2><div class="muted small">{levelLabel(v.level)} · {v.device}{v.mountpoint ? ` · ${v.mountpoint}` : ''}</div></div>
          <span class="spacer"></span>
          <button class="icon-btn" onclick={() => (selected = null)} aria-label={t('Close')}><Icon name="x" size={16} /></button>
        </div>

        {#if v.sync}
          <div class="sync">
            <span>{syncText(v.sync.action)}: <strong>{v.sync.percent.toFixed(1)}%</strong>
              {#if v.sync.speedKBs}<span class="muted">· {formatBytes(v.sync.speedKBs * 1024)}/s · {t('about {time} left', { time: formatDuration(v.sync.etaSeconds) })}</span>{/if}
            </span>
            <span class="meter"><span style="width: {v.sync.percent}%"></span></span>
            {#if v.sync.action === 'resync'}<span class="muted small">{t('The volume is usable during the initial sync, just slower.')}</span>{/if}
          </div>
        {/if}
        {#if v.status === 'degraded' && missing(v) > 0}
          <p class="warn-box">{t('A disk is missing: the volume has no redundancy left. Add a disk to start the rebuild.')}</p>
        {/if}
        {#if !v.managed && !v.mountpoint}
          {#if v.filesystem === 'ext4' || v.filesystem === 'btrfs'}
            <div class="notice-box">
              <p><strong>{t('Found on the disks.')}</strong> {v.lv ? t('This volume was made by another system (e.g. the original ReadyNAS OS), with LVM ({lv}).', { lv: v.lv }) : t('This volume was made by another system (e.g. the original ReadyNAS OS).')} {t('Use it as is: its files stay where they are.')}</p>
              <p class="muted small">{t('The array gets the name below, its {fs} filesystem is checked, then mounted. Then create shares on it, one per existing folder you want to share.', { fs: v.filesystem })}</p>
              <div class="add">
                <label>{t('Name')} <input bind:value={adoptName} placeholder="data" maxlength="16" /></label>
                <button onclick={() => run(() => startJob(`/api/volumes/${encodeURIComponent(v.name)}/adopt`, { name: adoptName }))} disabled={busy || !adoptName}>{t('Use this volume')}</button>
              </div>
            </div>
          {:else}
            <p class="hint">{t('Made by another system ({fs}): nothing to mount. Delete it to reuse its disk space.', { fs: v.filesystem || t('no filesystem') })}</p>
          {/if}
        {/if}
        {#if v.canGrowFS}
          <div class="warn-box">
            <p>{t('The filesystem uses only {used} of the {size} volume.', { used: formatBytes(v.totalBytes), size: formatBytes(v.sizeBytes) })}</p>
            <button class="secondary" onclick={() => run(() => startJob(`/api/volumes/${encodeURIComponent(v.name)}/grow-fs`, {}))} disabled={busy}>{t('Grow filesystem')}</button>
          </div>
        {/if}

        <div>
          <h3>{t('Disks')}</h3>
          <table class="members">
            <tbody>
              {#each v.members as m (m.device)}
                {@const l = memberLabel(m)}
                {@const d = disks.find((x) => x.name === m.disk)}
                <tr>
                  <td><strong>{d?.bay ? t('Bay {n}', { n: d.bay }) : m.disk}</strong> <span class="muted small">{m.device}</span></td>
                  <td><span class="badge {l.cls}">{l.text}</span></td>
                  <td class="right">{#if v.level !== 'raid0'}<button class="link danger-text" onclick={() => askRemove(v, m)} disabled={busy}>{t('Remove')}</button>{/if}</td>
                </tr>
              {/each}
              {#each Array(missing(v)) as _}<tr><td colspan="3" class="error small">{t('Missing disk')}</td></tr>{/each}
            </tbody>
          </table>
        </div>

        {#if v.level !== 'raid0' && candidates(v).length}
          {@const choice = addChoice[v.name] ?? candidates(v)[0].name}
          {@const isSpare = spares(v).some((m) => m.disk === choice)}
          <div class="stack add-disk">
            <h3>{t('Add a disk')}</h3>
            <select bind:value={addChoice[v.name]} aria-label={t('Disk to add')}>
              {#each candidates(v) as c (c.name)}<option value={c.name}>{c.label}</option>{/each}
            </select>
            <div class="add">
              {#if v.degraded > 0}
                {#if !isSpare}<button class="secondary" onclick={() => run(() => startJob(`/api/volumes/${v.name}/add`, { disk: choice }))} disabled={busy}>{t('Add & rebuild')}</button>{/if}
              {:else}
                {#if canExpand(v)}<button onclick={() => askExpand(v, choice)} disabled={busy}>{t('Expand volume')}</button>{/if}
                {#if !isSpare}<button class="secondary" onclick={() => run(() => startJob(`/api/volumes/${v.name}/add`, { disk: choice }))} disabled={busy}>{t('Add as hot spare')}</button>{/if}
              {/if}
            </div>
            <p class="hint">{v.degraded > 0 ? t('Replaces the missing disk.') : canExpand(v) ? t("Expand adds the disk's space. A hot spare stays idle and takes over when a disk fails.") : t('A hot spare stays idle and takes over when a disk fails.')}</p>
          </div>
        {/if}

        <VolumeMaintenance volume={v} {busy} onJob={(j) => (job = j)} />

        <div class="actions">
          <button class="secondary danger-text" onclick={() => askDelete(v)} disabled={busy}>{t('Delete volume')}</button>
        </div>
      </aside>
    {/if}
  </div>
</main>

{#if dialog}<ConfirmDialog {...dialog} onCancel={() => (dialog = null)} />{/if}
{#if smart}<TextDialog title={smart.title} text={smart.text} onClose={() => (smart = null)} />{/if}

<style>
  .system-line { display: flex; gap: 0.75rem; align-items: flex-start; }
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
