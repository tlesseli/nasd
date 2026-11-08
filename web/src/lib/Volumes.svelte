<script>
  import { onMount } from 'svelte'
  import { api, formatBytes, formatDuration } from '../api.js'
  import CreateVolume from './CreateVolume.svelte'
  import JobPanel from './JobPanel.svelte'
  import ConfirmDialog from './ConfirmDialog.svelte'

  let { onSessionLost } = $props()

  let volumes = $state([])
  let disks = $state([])
  let loaded = $state(false)
  let error = $state('')
  let job = $state(null) // the background job being shown
  let creating = $state(false)
  let dialog = $state(null) // props for ConfirmDialog, or null
  let addChoice = $state({}) // volume name -> chosen disk

  async function refresh() {
    try {
      ;[volumes, disks] = await Promise.all([api.get('/api/volumes'), api.get('/api/disks')])
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
    return () => clearInterval(timer)
  })

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
      onConfirm: () => startJob(`/api/volumes/${v.name}/delete`, { confirm: v.name }),
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
      onConfirm: () => startJob(`/api/volumes/${v.name}/remove`, { disk: m.disk }),
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
</script>

<main class="page">
  <div class="page-head">
    <h1>Volumes</h1>
    <span class="spacer"></span>
    {#if !creating}
      <button onclick={() => (creating = true)} disabled={busy || !freeDisks.length}
        title={freeDisks.length ? '' : 'No free disks'}>New volume</button>
    {/if}
  </div>

  {#if error}<p class="error">{error}</p>{/if}

  {#if job}
    <JobPanel {job} onFinished={refresh} onClose={() => (job = null)} />
  {/if}

  {#if creating}
    <CreateVolume {freeDisks}
      onCreate={(req) => startJob('/api/volumes', req)}
      onCancel={() => (creating = false)} />
  {/if}

  {#each volumes as v (v.device)}
    <article class="card volume">
      <div class="volume-head">
        <h2>{v.name}</h2>
        <span class="badge {statusClass[v.status] ?? 'neutral'}">{v.status}</span>
        <span class="muted">{levelLabel(v.level)} · {v.filesystem || 'no filesystem'} · {v.device}</span>
        <span class="spacer"></span>
        <button class="secondary danger-text" onclick={() => askDelete(v)} disabled={busy}>Delete</button>
      </div>

      {#if v.mountpoint && v.totalBytes}
        {@const pct = Math.round((100 * v.usedBytes) / v.totalBytes)}
        <div class="usage">
          <span class="meter big"><span style="width: {pct}%" class:full={pct >= 90}></span></span>
          <span><strong>{formatBytes(v.usedBytes)}</strong> used of {formatBytes(v.totalBytes)} · {formatBytes(v.availBytes)} free · <span class="mono">{v.mountpoint}</span></span>
        </div>
      {:else}
        <p class="muted">{formatBytes(v.sizeBytes)} · not mounted</p>
      {/if}

      {#if v.sync}
        <div class="sync">
          <span>{syncLabel[v.sync.action] ?? v.sync.action}: <strong>{v.sync.percent.toFixed(1)}%</strong>
            {#if v.sync.speedKBs}
              <span class="muted">· {formatBytes(v.sync.speedKBs * 1024)}/s · about {formatDuration(v.sync.etaSeconds)} left</span>
            {/if}
          </span>
          <span class="meter"><span style="width: {v.sync.percent}%"></span></span>
          {#if v.sync.action === 'resync'}
            <span class="muted small">The volume is usable during the initial sync, just slower.</span>
          {/if}
        </div>
      {/if}

      {#if v.status === 'degraded' && missing(v) > 0}
        <p class="warn-box">A disk is missing: the volume has no redundancy left. Add a disk to start the rebuild.</p>
      {/if}

      <table class="members">
        <thead><tr><th>Disk</th><th>Partition</th><th>State</th><th></th></tr></thead>
        <tbody>
          {#each v.members as m (m.device)}
            {@const l = memberLabel(m)}
            <tr>
              <td><strong>{m.disk}</strong></td>
              <td class="mono">{m.device}</td>
              <td><span class="badge {l.cls}">{l.text}</span></td>
              <td class="right">
                {#if v.level !== 'raid0'}
                  <button class="link danger-text" onclick={() => askRemove(v, m)} disabled={busy}>Remove</button>
                {/if}
              </td>
            </tr>
          {/each}
          {#each Array(missing(v)) as _, i}
            <tr class="missing"><td colspan="4">Missing disk</td></tr>
          {/each}
        </tbody>
      </table>

      {#if v.canGrowFS}
        <div class="warn-box row-box">
          <span>The filesystem uses only {formatBytes(v.totalBytes)} of the {formatBytes(v.sizeBytes)} volume (for example after an expansion that finished while the NAS was restarting).</span>
          <button class="secondary" onclick={() => run(() => startJob(`/api/volumes/${v.name}/grow-fs`, {}))} disabled={busy}>Grow filesystem</button>
        </div>
      {/if}

      {#if v.level !== 'raid0' && candidates(v).length}
        {@const choice = addChoice[v.name] ?? candidates(v)[0].name}
        {@const isSpare = spares(v).some((m) => m.disk === choice)}
        <div class="add">
          <select bind:value={addChoice[v.name]}>
            {#each candidates(v) as c (c.name)}
              <option value={c.name}>{c.label}</option>
            {/each}
          </select>
          {#if v.degraded > 0}
            {#if !isSpare}
              <button class="secondary" onclick={() => run(() => startJob(`/api/volumes/${v.name}/add`, { disk: choice }))} disabled={busy}>Add &amp; rebuild</button>
              <span class="muted small">Replaces the missing disk.</span>
            {/if}
          {:else}
            {#if canExpand(v)}
              <button onclick={() => askExpand(v, choice)} disabled={busy}>Expand volume</button>
            {/if}
            {#if !isSpare}
              <button class="secondary" onclick={() => run(() => startJob(`/api/volumes/${v.name}/add`, { disk: choice }))} disabled={busy}>Add as hot spare</button>
            {/if}
            <span class="muted small">
              {canExpand(v) ? 'Expand adds the disk\'s space to the volume. A hot spare stays idle and replaces a failing disk automatically.'
                : 'A hot spare stays idle and replaces a failing disk automatically.'}
            </span>
          {/if}
        </div>
      {/if}
    </article>
  {:else}
    {#if loaded && !creating}
      <div class="card empty">
        <p><strong>No volumes yet.</strong></p>
        <p class="muted">A volume combines disks into one RAID-protected space. Create one to start storing files.</p>
      </div>
    {/if}
  {/each}

  {#if dirtyDisks.length}
    <h2>Disks with old data</h2>
    <p class="muted small">These disks contain partitions or RAID signatures but aren't part of any active volume (for example, leftovers from the original ReadyNAS OS). Erase them to make them available.</p>
    <div class="card list">
      {#each dirtyDisks as d (d.name)}
        <div class="row">
          <strong>{d.name}</strong>
          <span class="muted">{formatBytes(d.sizeBytes)} · {d.model} · {d.usage || 'partitioned'}</span>
          <span class="spacer"></span>
          <button class="secondary danger-text" onclick={() => askErase(d)} disabled={busy}>Erase</button>
        </div>
      {/each}
    </div>
  {/if}
</main>

{#if dialog}
  <ConfirmDialog {...dialog} onCancel={() => (dialog = null)} />
{/if}
