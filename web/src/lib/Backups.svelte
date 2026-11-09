<script>
  import { onMount } from 'svelte'
  import { api, formatBytes } from '../api.js'
  import BackupForm from './BackupForm.svelte'
  import JobPanel from './JobPanel.svelte'
  import ConfirmDialog from './ConfirmDialog.svelte'
  import { t, N_ } from '../i18n.svelte.js'

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

  function askThaw(b) {
    dialog = {
      title: t('Request retrieval of {name}?', { name: b.name }),
      message: t('Archived files are frozen: the provider must first prepare them (bulk tier, the cheapest: usually 12 to 48 hours). They then stay readable for 7 days; run a job in the direction "into the share" to restore them. Retrieval is billed per GB by the provider.'),
      confirmLabel: t('Request retrieval'),
      danger: false,
      onConfirm: async () => {
        job = await api.post(`/api/backups/${b.id}/thaw`, { days: 7 })
        dialog = null
      },
    }
  }

  function askDelete(b) {
    dialog = {
      title: t('Delete backup job {name}?', { name: b.name }),
      message: t('Only the job is deleted: files already backed up stay where they are.'),
      confirmLabel: t('Delete job'),
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
      title: t('Format USB disk {disk}?', { disk: f.disk }),
      message: t('Everything on {disk} is erased and it gets one {fs} filesystem named {label}.', { disk: f.disk, fs: f.fstype === 'ext4' ? t('ext4 (Linux)') : t('exFAT (Windows/Mac/Linux)'), label: f.label }),
      expected: f.disk,
      confirmLabel: t('Format'),
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
      copyMsg = { ok: true, text: t('Saved.') }
    } catch (e) {
      copyMsg = { ok: false, text: e.message }
    }
  }

  const days = [N_('Sun'), N_('Mon'), N_('Tue'), N_('Wed'), N_('Thu'), N_('Fri'), N_('Sat')]
  const hhmm = (s) => `${String(s.hour).padStart(2, '0')}:${String(s.minute).padStart(2, '0')}`
  function scheduleText(s) {
    switch (s.kind) {
      case 'hourly': return t('Every hour at :{minute}', { minute: String(s.minute).padStart(2, '0') })
      case 'daily': return t('Every day at {time}', { time: hhmm(s) })
      case 'weekly': return t('Every {day} at {time}', { day: t(days[s.weekday]), time: hhmm(s) })
      default: return t('Manual')
    }
  }
  function targetText(tg) {
    switch (tg.kind) {
      case 'usb': {
        const v = usb.flatMap((d) => d.volumes).find((v) => v.uuid === tg.usbUUID)
        return t('USB disk {disk} · {path}', { disk: v ? v.label || v.device : t('(not plugged in)'), path: tg.path })
      }
      case 'ssh': return `${tg.user}@${tg.host}:${tg.path}`
      case 's3': return `${{ aws: 'AWS', scaleway: 'Scaleway', other: 'S3' }[tg.provider]} · ${tg.bucket}${tg.path ? '/' + tg.path : ''} · ${tg.storageClass || t('default class')}`
      default: return `rsync://${tg.user ? tg.user + '@' : ''}${tg.host}/${tg.module}${tg.path ? '/' + tg.path : ''}`
    }
  }
  const when = (d) => new Date(d).toLocaleString()
  let busy = $derived(job?.status === 'running')
</script>

<main class="page">
  <div class="page-head">
    <h1>{t('Backups')}</h1>
    <span class="spacer"></span>
    {#if !editing}
      <button onclick={() => (editing = 'new')} disabled={!shares.length} title={shares.length ? '' : t('Create a share first')}>{t('New backup job')}</button>
    {/if}
  </div>
  <p class="muted">{t('RAID survives a dead disk; backups survive everything else: a deleted folder, ransomware, a dead NAS. Keep at least one copy outside the NAS.')}</p>

  {#if error}<p class="error">{error}</p>{/if}
  {#if job}<JobPanel {job} onFinished={refresh} onClose={() => (job = null)} />{/if}

  {#if editing}
    <BackupForm job={editing === 'new' ? null : editing} {shares} {usb} {publicKey} onSave={save} onCancel={() => (editing = null)} />
  {/if}

  {#each backups as b (b.id)}
    <article class="card volume">
      <div class="volume-head">
        <h2>{b.name}</h2>
        {#if b.lastRun.status === 'done'}<span class="badge good">{t('OK')}</span>
        {:else if b.lastRun.status === 'failed'}<span class="badge bad">{t('Failed')}</span>
        {:else}<span class="badge neutral">{t('Never ran')}</span>{/if}
        {#if b.mirror}<span class="badge neutral">{t('Mirror')}</span>{/if}
        {#if b.target.kind === 's3'}<span class="badge info">{t('Cloud')}</span>{#if b.target.encrypted}<span class="badge good">{t('Encrypted')}</span>{/if}{/if}
        <span class="spacer"></span>
        <button onclick={() => runNow(b)} disabled={busy}>{t('Run now')}</button>
        {#if b.target.kind === 's3'}
          <button class="secondary" onclick={() => askThaw(b)} disabled={busy} title={t('Make archived files readable again before restoring')}>{t('Request retrieval')}</button>
        {/if}
        <button class="secondary" onclick={() => (editing = b)} disabled={!!editing}>{t('Edit')}</button>
        <button class="secondary danger-text" onclick={() => askDelete(b)}>{t('Delete')}</button>
      </div>
      <dl class="share-info">
        <dt>{b.direction === 'push' ? t('Copies') : t('Into')}</dt><dd>{t('share')} <strong>{b.share}</strong></dd>
        <dt>{b.direction === 'push' ? t('To') : t('From')}</dt><dd class="mono">{targetText(b.target)}</dd>
        <dt>{t('Schedule')}</dt><dd>{scheduleText(b.schedule)}</dd>
        {#if b.lastRun.status}
          <dt>{t('Last run')}</dt><dd>{when(b.lastRun.started)} · {b.lastRun.summary || b.lastRun.status}</dd>
        {/if}
      </dl>
    </article>
  {:else}
    {#if loaded && !editing}
      <div class="card empty">
        <p><strong>{t('No backup jobs yet.')}</strong></p>
        <p class="muted">{t('Copy a share to a USB disk or another computer, on a schedule. Or bring data in from an old NAS.')}</p>
      </div>
    {/if}
  {/each}

  {#if usbCopy}
    <h2>{t('Copy USB disks when plugged in')}</h2>
    <form class="card create" onsubmit={saveUSBCopy}>
      <label class="check">
        <input type="checkbox" bind:checked={usbCopy.enabled} />
        <span><strong>{t("Copy the content of a USB disk when it's plugged in")}</strong> <span class="muted small">{t('· e.g. a camera card: photos land in a share without a computer. Each copy goes into a new dated folder; the USB disk is only read, never modified.')}</span></span>
      </label>
      {#if usbCopy.enabled}
        <div class="two-cols">
          <label>{t('Into share')}
            <select bind:value={usbCopy.share}>{#each shares as s (s.name)}<option>{s.name}</option>{/each}</select></label>
          <label>{t('Folder')} <span class="hint">{t('(optional)')}</span> <input bind:value={usbCopy.path} placeholder={t('Imports')} /></label>
          <label>{t('Files belong to')}
            <select bind:value={usbCopy.owner}>
              <option value="">{t('Everyone with access to the share')}</option>
              {#each users as u (u.name)}<option value={u.name}>{u.name}</option>{/each}
            </select></label>
        </div>
        <p class="hint">{t('USB disks used by a backup job are never copied.')}</p>
      {/if}
      {#if copyMsg}<p class={copyMsg.ok ? 'good-text small' : 'error small'}>{copyMsg.text}</p>{/if}
      <div class="actions"><button type="submit">{t('Save')}</button></div>
    </form>
  {/if}

  <h2>{t('USB disks')}</h2>
  <div class="card list">
    {#each usb as d (d.disk)}
      <div class="row">
        <strong>{d.disk}</strong>
        <span class="muted small">{d.model} · {formatBytes(d.sizeBytes)} ·
          {d.volumes.length ? d.volumes.map((v) => `${v.label || v.device} (${v.fstype})`).join(', ') : t('not formatted')}</span>
        <span class="spacer"></span>
        <button class="secondary" onclick={() => (formatting = { disk: d.disk, fstype: 'exfat', label: 'BACKUP' })} disabled={busy}>{t('Format…')}</button>
      </div>
      {#if formatting?.disk === d.disk}
        <div class="row">
          <select bind:value={formatting.fstype} aria-label={t('Filesystem')}>
            <option value="exfat">{t('exFAT: readable by Windows, Mac and Linux')}</option>
            <option value="ext4">{t('ext4: Linux only, keeps permissions')}</option>
          </select>
          <input bind:value={formatting.label} maxlength="11" aria-label={t('Label')} style="width: 9rem" />
          <button class="danger" onclick={askFormat}>{t('Format')}</button>
          <button class="link" onclick={() => (formatting = null)}>{t('Cancel')}</button>
        </div>
      {/if}
    {:else}
      <p class="muted small">{t('No USB disk plugged in.')}</p>
    {/each}
  </div>
</main>

{#if dialog}<ConfirmDialog {...dialog} onCancel={() => (dialog = null)} />{/if}
