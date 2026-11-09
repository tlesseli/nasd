<script>
  import { onMount } from 'svelte'
  import { api, formatBytes } from '../api.js'
  import ShareForm from './ShareForm.svelte'
  import ConfirmDialog from './ConfirmDialog.svelte'
  import HomeFolders from './HomeFolders.svelte'
  import PublicFolder from './PublicFolder.svelte'
  import JobPanel from './JobPanel.svelte'
  import Icon from '../ui/Icon.svelte'
  import { t, N_ } from '../i18n.svelte.js'

  let { hostname, onSessionLost, params = {} } = $props()

  let shares = $state([])
  let users = $state([])
  let volumes = $state([])
  let settings = $state({})
  let services = $state([])
  let unshared = $state([])
  let job = $state(null) // folder deletion in progress
  let preset = $state(null) // {name, volume}: new share for an existing folder
  let loaded = $state(false)
  let error = $state('')
  let notice = $state('')
  let dialog = $state(null)
  let selected = $state(null) // share name, or '__new__'
  let filter = $state('all')
  let text = $state('')

  $effect(() => {
    if (params.sel) selected = params.sel
  })

  async function refresh() {
    try {
      ;[shares, users, volumes, settings, services, unshared] = await Promise.all([
        api.get('/api/shares'), api.get('/api/users'), api.get('/api/volumes'), api.get('/api/settings'), api.get('/api/services'),
        api.get('/api/shares/unshared'),
      ])
      loaded = true
    } catch (e) {
      if (e.status === 401) onSessionLost()
      else error = e.message
    }
  }
  onMount(refresh)

  const mountedVolumes = $derived(volumes.filter((v) => v.mountpoint && v.managed && !v.system))
  const sel = $derived(shares.find((s) => s.name === selected))
  const smbSessions = $derived(services.find((s) => s.unit === 'smbd')?.detail ?? '')

  const has = {
    smb: (s) => !s.smbDisabled,
    ftp: (s) => s.ftp,
    rsync: (s) => !!s.rsync,
  }
  const counts = $derived({ all: shares.length, smb: shares.filter(has.smb).length, ftp: shares.filter(has.ftp).length, rsync: shares.filter(has.rsync).length })
  const visible = $derived(shares.filter((s) => (filter === 'all' || has[filter](s)) &&
    (!text.trim() || (s.name + ' ' + s.description).toLowerCase().includes(text.trim().toLowerCase()))))

  function newShareFor(f) {
    preset = { name: f.name, volume: f.volume }
    selected = '__new__'
  }

  async function save(req) {
    if (selected === '__new__') {
      await api.post('/api/shares', req)
      notice = t('Share {name} created.', { name: req.name })
      selected = req.name
      preset = null
    } else {
      await api.post(`/api/shares/${selected}`, req)
      notice = t('Share {name} saved.', { name: selected })
    }
    refresh()
  }

  // A folder no share uses: erased with its files (typed name + admin
  // password). The NAS runs it as a job, shown at the top of the page.
  function askDeleteFolder(f) {
    dialog = {
      title: t('Delete folder {name}?', { name: f.name }),
      message: t('The folder /{volume}/{name} and every file in it are erased. This cannot be undone.', { volume: f.volume, name: f.name }),
      confirmLabel: t('Delete folder'),
      expected: f.name,
      askPassword: true,
      onConfirm: async (password) => {
        job = await api.post('/api/folders/delete', { volume: f.volume, name: f.name, password })
        dialog = null
      },
    }
  }

  function askDelete(sh) {
    dialog = {
      title: t('Stop sharing {name}?', { name: sh.name }),
      message: t('The share disappears from the network. Its files are kept in {path}; create a share with the same name on the same volume to get them back.', { path: sh.path }),
      confirmLabel: t('Delete share'),
      expected: sh.name,
      askPassword: true,
      onConfirm: async (password) => {
        await api.post(`/api/shares/${sh.name}/delete`, { password })
        dialog = null
        selected = null
        notice = t('Share {name} deleted. Files kept in {path}.', { name: sh.name, path: sh.path })
        refresh()
      },
    }
  }

  function summary(sh) {
    const entries = Object.entries(sh.access)
    const parts = []
    const rw = entries.filter(([, a]) => a === 'rw').map(([u]) => u)
    const ro = entries.filter(([, a]) => a === 'ro').map(([u]) => u)
    if (rw.length) parts.push(rw.length > 2 ? t('{n} users edit', { n: rw.length }) : t('{names} edit', { names: rw.join(', ') }))
    if (ro.length) parts.push(ro.length > 2 ? t('{n} users read', { n: ro.length }) : t('{names} read', { names: ro.join(', ') }))
    if (sh.guestRead) parts.push(t('guests read'))
    return parts.length ? parts.join(' · ') : t('Nobody has access yet')
  }
  function status(sh) {
    if (!sh.available) return ['bad', N_('Volume not mounted')]
    if (sh.offline) return ['warn', N_('Offline')]
    if (sh.smbDisabled && !sh.ftp && !sh.rsync) return ['neutral', N_('Disabled')]
    return ['good', N_('Online')]
  }
  const svcOff = (k) => (k === 'smb' ? settings.smbDisabled : k === 'ftp' ? !settings.ftpEnabled : !settings.rsyncEnabled)
</script>

<main class="page">
  <div class="page-head">
    <div>
      <div class="eyebrow">{shares.length === 1 ? t('{n} share', { n: 1 }) : t('{n} shares', { n: shares.length })}{smbSessions ? ` · ${smbSessions.replace('sessions', 'SMB sessions')}` : ''}</div>
      <h1>{t('Shares')}</h1>
    </div>
    <div class="actions">
      <button onclick={() => ((preset = null), (selected = '__new__'))} disabled={!mountedVolumes.length} title={mountedVolumes.length ? '' : t('Create a volume first')}>{t('New share')}</button>
    </div>
  </div>

  {#if error}<p class="error">{error}</p>{/if}
  {#if notice}<p class="notice">{notice}</p>{/if}
  {#if job}<JobPanel {job} onFinished={refresh} onClose={() => (job = null)} />{/if}

  <div class="filters">
    <div class="segmented-btns" role="group" aria-label={t('Filter by service')}>
      {#each [['all', N_('All')], ['smb', 'SMB'], ['ftp', 'FTP'], ['rsync', 'rsync']] as [k, label]}
        <button class:on={filter === k} aria-pressed={filter === k} onclick={() => (filter = k)}>{t(label)} {counts[k]}</button>
      {/each}
    </div>
    <span class="spacer"></span>
    <input class="filter-input" bind:value={text} placeholder={t('Filter shares…')} aria-label={t('Filter shares')} />
  </div>

  <div class:with-panel={!!selected}>
    <div class="stack">
      <div class="share-grid">
        {#each visible as sh (sh.name)}
          {@const [cls, st] = status(sh)}
          <button class="card share-card" class:selected={selected === sh.name} onclick={() => (selected = selected === sh.name ? null : sh.name)}
            aria-pressed={selected === sh.name}>
            <div class="sc-top"><span class="folder"><Icon name="folder" size={16} /></span><span class="spacer"></span><span class="badge {cls}">{t(st)}</span></div>
            <div class="sc-name">{sh.name}</div>
            <div class="mono muted small">/{sh.volume}/{sh.name}</div>
            <div class="small sc-access">{summary(sh)}</div>
            <div class="sc-foot">
              <div class="badges">
                {#if has.smb(sh)}<span class="badge mono" class:off={svcOff('smb')}>SMB</span>{/if}
                {#if sh.ftp}<span class="badge mono" class:off={svcOff('ftp')}>FTP</span>{/if}
                {#if sh.inPublic && !sh.offline}<span class="badge mono">{t('Public')}</span>{/if}
                {#if sh.rsync}<span class="badge mono" class:off={svcOff('rsync')}>rsync{sh.rsync === 'rw' ? ' r/w' : ''}</span>{/if}
                {#if !has.smb(sh) && !sh.ftp && !sh.rsync}<span class="muted small">{t('no service')}</span>{/if}
              </div>
              <span class="small">{sh.size ? formatBytes(sh.size.bytes) : '—'}</span>
            </div>
          </button>
        {:else}
          {#if loaded}
            <div class="card empty">
              {#if shares.length}<p class="muted">{t('No share matches this filter.')}</p>
              {:else}<p><strong>{t('No shares yet.')}</strong></p><p class="muted">{t('A share is a folder on a volume that computers on your network can open, like a network drive.')}</p>{/if}
            </div>
          {/if}
        {/each}
      </div>
      {#if unshared.length}
        <div class="card">
          <h2>{t('Folders not shared yet')}</h2>
          <p class="muted small">{t('These folders are already on your volumes (for example from the original ReadyNAS OS). Share one to make it visible on the network: its files stay as they are.')}</p>
          <table class="members">
            <tbody>
              {#each unshared as f (f.volume + '/' + f.name)}
                <tr>
                  <td><Icon name="folder" size={14} /> <strong>{f.name}</strong></td>
                  <td class="mono muted small">/{f.volume}/{f.name}</td>
                  <td class="right actions-cell">
                    <button class="secondary" onclick={() => newShareFor(f)}>{t('Share')}</button>
                    <button class="link danger-text" onclick={() => askDeleteFolder(f)} disabled={job?.status === 'running'}>{t('Delete')}</button>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
      <div class="grid cols-2">
        <div class="card"><HomeFolders /></div>
        <div class="card"><PublicFolder /></div>
      </div>
      {#if settings.smbDisabled || (!settings.ftpEnabled && shares.some((x) => x.ftp)) || (!settings.rsyncEnabled && shares.some((x) => x.rsync))}
        <p class="hint">{t('Struck-through services are switched off for the whole NAS:')} <a href="#/system/services">{t('System Settings')} → {t('Services')}</a>.</p>
      {/if}
    </div>

    {#if selected}
      <aside class="card panel" aria-label={sel ? t('Share {name}', { name: sel.name }) : t('New share')}>
        <div class="panel-head">
          <div>
            <h2>{sel ? sel.name : t('New share')}</h2>
            {#if sel && !sel.smbDisabled}<div class="mono muted small copyable">\\{hostname}\{sel.name}</div>{/if}
          </div>
          <span class="spacer"></span>
          <button class="icon-btn" onclick={() => (selected = null)} aria-label={t('Close')}><Icon name="x" size={16} /></button>
        </div>
        {#if sel}
          <div class="grid cols-2 tiles">
            <div class="tile"><div class="label">{t('Volume')}</div><div class="value mono">{sel.volume}</div></div>
            <div class="tile"><div class="label">{t('Size')}</div><div class="value">{sel.size ? formatBytes(sel.size.bytes) : t('measuring…')}</div></div>
            <div class="tile"><div class="label">{t('Recycle bin')}</div><div class="value">{sel.recycleBin ? t('On') : t('Off')}</div></div>
            <div class="tile"><div class="label">{t('Network browsing')}</div><div class="value">{sel.smbDisabled ? '—' : sel.hidden ? t('Not listed') : t('Listed')}</div></div>
          </div>
          <dl class="share-info small">
            {#if !sel.smbDisabled}<dt>{t('Mac / Linux')}</dt><dd class="mono copyable">smb://{hostname}.local/{sel.name}</dd>{/if}
            {#if sel.ftp}<dt>FTP</dt><dd class="mono copyable">ftp://{hostname}.local/{sel.name}/</dd>{/if}
            {#if sel.rsync}<dt>rsync</dt><dd class="mono copyable">rsync://{sel.rsyncUser ? sel.rsyncUser + '@' : ''}{hostname}.local/{sel.name}/</dd>{/if}
            <dt>{t('Folder')}</dt><dd class="mono">{sel.path}</dd>
          </dl>
        {/if}
        {#key selected + (preset ? preset.volume + '/' + preset.name : '')}
          {#if selected === '__new__' || sel}
            <ShareForm bare share={sel ?? null} {preset} volumes={mountedVolumes} {users} {settings}
              onSave={save} onCancel={() => (selected = null)} onDelete={sel ? () => askDelete(sel) : undefined} />
          {/if}
        {/key}
      </aside>
    {/if}
  </div>
</main>

{#if dialog}<ConfirmDialog {...dialog} onCancel={() => (dialog = null)} />{/if}

<style>
  .actions-cell { white-space: nowrap; }
  .actions-cell .link { margin-left: 0.4rem; }
  .filters { display: flex; align-items: center; gap: 0.75rem; margin-bottom: 14px; flex-wrap: wrap; }
  .filter-input { width: 16rem; max-width: 100%; }
  .share-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(260px, 1fr)); gap: 14px; }
  .share-card { text-align: left; color: var(--text); font-weight: 400; display: flex; flex-direction: column; gap: 0.25rem; cursor: pointer; background: var(--surface); }
  .share-card:hover:not(:disabled) { filter: none; border-color: var(--border-strong); }
  .share-card.selected { border-color: var(--accent); box-shadow: 0 0 0 1px var(--accent); }
  .sc-top { display: flex; align-items: center; margin-bottom: 0.5rem; }
  .folder { width: 34px; height: 34px; border-radius: 8px; display: grid; place-items: center; background: var(--surface-2); border: 1px solid var(--border); color: var(--muted); }
  .sc-name { font-size: 1.05rem; font-weight: 600; }
  .sc-access { color: var(--text-2); margin: 0.5rem 0 0.7rem; }
  .sc-foot { display: flex; align-items: center; justify-content: space-between; gap: 0.5rem; border-top: 1px solid var(--border); padding-top: 0.7rem; margin-top: auto; }
  .tiles { gap: 8px; }
</style>
