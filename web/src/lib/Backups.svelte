<script>
  import { onMount } from 'svelte'
  import { api, formatBytes } from '../api.js'
  import BackupForm from './BackupForm.svelte'
  import JobPanel from './JobPanel.svelte'
  import ConfirmDialog from './ConfirmDialog.svelte'

  let { onSessionLost } = $props()

  let backups = $state([])
  let shares = $state([])
  let usb = $state([])
  let publicKey = $state('')
  let loaded = $state(false)
  let error = $state('')
  let editing = $state(null) // null | 'new' | job
  let job = $state(null) // running background job shown in the panel
  let dialog = $state(null)
  let formatting = $state(null) // {disk, fstype, label}
  let usbCopy = $state(null)
  let users = $state([])
  let copyMsg = $state(null)

  async function refresh() {
    try {
      ;[backups, shares, usb] = await Promise.all([api.get('/api/backups'), api.get('/api/shares'), api.get('/api/usb')])
      loaded = true
    } catch (e) {
      if (e.status === 401) onSessionLost()
      else error = e.message
    }
  }

  onMount(() => {
    refresh()
    Promise.all([api.get('/api/usbcopy'), api.get('/api/users')]).then(([c, u]) => {
      usbCopy = c
      users = u
    }).catch(() => {})
    api.get('/api/backups/key').then((r) => (publicKey = r.publicKey)).catch(() => {})
    api.get('/api/jobs').then((jobs) => {
      if (jobs[0]?.status === 'running') job = jobs[0]
    }).catch(() => {})
    const timer = setInterval(refresh, 5000) // notices USB disks being plugged in
    return () => clearInterval(timer)
  })

  async function save(b) {
    await api.post('/api/backups', b)
    editing = null
    refresh()
  }

  async function runNow(b) {
    error = ''
    try {
      job = await api.post(`/api/backups/${b.id}/run`)
    } catch (e) {
      error = e.message
    }
  }

  function askDelete(b) {
    dialog = {
      title: `Delete backup job ${b.name}?`,
      message: 'Only the job is deleted: files already backed up stay where they are.',
      confirmLabel: 'Delete job',
      onConfirm: async () => {
        await api.post(`/api/backups/${b.id}/delete`)
        dialog = null
        refresh()
      },
    }
  }

  function askFormat() {
    const f = formatting
    dialog = {
      title: `Format USB disk ${f.disk}?`,
      message: `Everything on ${f.disk} is erased and it gets one ${f.fstype === 'ext4' ? 'ext4 (Linux)' : 'exFAT (Windows/Mac/Linux)'} filesystem named ${f.label}.`,
      expected: f.disk,
      confirmLabel: 'Format',
      onConfirm: async () => {
        job = await api.post(`/api/usb/${f.disk}/format`, { fstype: f.fstype, label: f.label, confirm: f.disk })
        dialog = null
        formatting = null
      },
    }
  }

  async function saveUSBCopy(event) {
    event.preventDefault()
    if (usbCopy.enabled && !usbCopy.share) usbCopy.share = shares[0]?.name ?? ''
    try {
      await api.post('/api/usbcopy', usbCopy)
      copyMsg = { ok: true, text: 'Saved.' }
    } catch (e) {
      copyMsg = { ok: false, text: e.message }
    }
  }

  const days = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat']
  const hhmm = (s) => `${String(s.hour).padStart(2, '0')}:${String(s.minute).padStart(2, '0')}`
  function scheduleText(s) {
    switch (s.kind) {
      case 'hourly': return `Every hour at :${String(s.minute).padStart(2, '0')}`
      case 'daily': return `Every day at ${hhmm(s)}`
      case 'weekly': return `Every ${days[s.weekday]} at ${hhmm(s)}`
      default: return 'Manual'
    }
  }
  function targetText(t) {
    switch (t.kind) {
      case 'usb': {
        const v = usb.flatMap((d) => d.volumes).find((v) => v.uuid === t.usbUUID)
        return `USB disk ${v ? v.label || v.device : `(not plugged in)`} · ${t.path}`
      }
      case 'ssh': return `${t.user}@${t.host}:${t.path}`
      default: return `rsync://${t.user ? t.user + '@' : ''}${t.host}/${t.module}${t.path ? '/' + t.path : ''}`
    }
  }
  const when = (d) => new Date(d).toLocaleString()
  let busy = $derived(job?.status === 'running')
</script>

<main class="page">
  <div class="page-head">
    <h1>Backups</h1>
    <span class="spacer"></span>
    {#if !editing}
      <button onclick={() => (editing = 'new')} disabled={!shares.length} title={shares.length ? '' : 'Create a share first'}>New backup job</button>
    {/if}
  </div>
  <p class="muted">RAID survives a dead disk; backups survive everything else: a deleted folder, ransomware, a dead NAS. Keep at least one copy outside the NAS.</p>

  {#if error}<p class="error">{error}</p>{/if}
  {#if job}<JobPanel {job} onFinished={refresh} onClose={() => (job = null)} />{/if}

  {#if editing}
    <BackupForm job={editing === 'new' ? null : editing} {shares} {usb} {publicKey} onSave={save} onCancel={() => (editing = null)} />
  {/if}

  {#each backups as b (b.id)}
    <article class="card volume">
      <div class="volume-head">
        <h2>{b.name}</h2>
        {#if b.lastRun.status === 'done'}<span class="badge good">OK</span>
        {:else if b.lastRun.status === 'failed'}<span class="badge bad">Failed</span>
        {:else}<span class="badge neutral">Never ran</span>{/if}
        {#if b.mirror}<span class="badge neutral">Mirror</span>{/if}
        <span class="spacer"></span>
        <button onclick={() => runNow(b)} disabled={busy}>Run now</button>
        <button class="secondary" onclick={() => (editing = b)} disabled={!!editing}>Edit</button>
        <button class="secondary danger-text" onclick={() => askDelete(b)}>Delete</button>
      </div>
      <dl class="share-info">
        <dt>{b.direction === 'push' ? 'Copies' : 'Into'}</dt><dd>share <strong>{b.share}</strong></dd>
        <dt>{b.direction === 'push' ? 'To' : 'From'}</dt><dd class="mono">{targetText(b.target)}</dd>
        <dt>Schedule</dt><dd>{scheduleText(b.schedule)}</dd>
        {#if b.lastRun.status}
          <dt>Last run</dt><dd>{when(b.lastRun.started)} · {b.lastRun.summary || b.lastRun.status}</dd>
        {/if}
      </dl>
    </article>
  {:else}
    {#if loaded && !editing}
      <div class="card empty">
        <p><strong>No backup jobs yet.</strong></p>
        <p class="muted">Copy a share to a USB disk or another computer, on a schedule. Or bring data in from an old NAS.</p>
      </div>
    {/if}
  {/each}

  {#if usbCopy}
    <h2>Copy USB disks when plugged in</h2>
    <form class="card create" onsubmit={saveUSBCopy}>
      <label class="check">
        <input type="checkbox" bind:checked={usbCopy.enabled} />
        <span><strong>Copy the content of a USB disk when it's plugged in</strong> <span class="muted small">· e.g. a camera card: photos land in a share without a computer. Each copy goes into a new dated folder; the USB disk is only read, never modified.</span></span>
      </label>
      {#if usbCopy.enabled}
        <div class="two-cols">
          <label>Into share
            <select bind:value={usbCopy.share}>{#each shares as s (s.name)}<option>{s.name}</option>{/each}</select></label>
          <label>Folder <span class="hint">(optional)</span> <input bind:value={usbCopy.path} placeholder="Imports" /></label>
          <label>Files belong to
            <select bind:value={usbCopy.owner}>
              <option value="">Everyone with access to the share</option>
              {#each users as u (u.name)}<option value={u.name}>{u.name}</option>{/each}
            </select></label>
        </div>
        <p class="hint">USB disks used by a backup job are never copied.</p>
      {/if}
      {#if copyMsg}<p class={copyMsg.ok ? 'good-text small' : 'error small'}>{copyMsg.text}</p>{/if}
      <div class="actions"><button type="submit">Save</button></div>
    </form>
  {/if}

  <h2>USB disks</h2>
  <div class="card list">
    {#each usb as d (d.disk)}
      <div class="row">
        <strong>{d.disk}</strong>
        <span class="muted small">{d.model} · {formatBytes(d.sizeBytes)} ·
          {d.volumes.length ? d.volumes.map((v) => `${v.label || v.device} (${v.fstype})`).join(', ') : 'not formatted'}</span>
        <span class="spacer"></span>
        <button class="secondary" onclick={() => (formatting = { disk: d.disk, fstype: 'exfat', label: 'BACKUP' })} disabled={busy}>Format…</button>
      </div>
      {#if formatting?.disk === d.disk}
        <div class="row">
          <select bind:value={formatting.fstype} aria-label="Filesystem">
            <option value="exfat">exFAT: readable by Windows, Mac and Linux</option>
            <option value="ext4">ext4: Linux only, keeps permissions</option>
          </select>
          <input bind:value={formatting.label} maxlength="11" aria-label="Label" style="width: 9rem" />
          <button class="danger" onclick={askFormat}>Format</button>
          <button class="link" onclick={() => (formatting = null)}>Cancel</button>
        </div>
      {/if}
    {:else}
      <p class="muted small">No USB disk plugged in.</p>
    {/each}
  </div>
</main>

{#if dialog}<ConfirmDialog {...dialog} onCancel={() => (dialog = null)} />{/if}
