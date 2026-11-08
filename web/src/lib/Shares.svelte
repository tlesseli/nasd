<script>
  import { onMount } from 'svelte'
  import { api } from '../api.js'
  import ShareForm from './ShareForm.svelte'
  import ConfirmDialog from './ConfirmDialog.svelte'

  let { hostname, onSessionLost } = $props()

  let shares = $state([])
  let users = $state([])
  let volumes = $state([])
  let settings = $state({ smbDisabled: false, rsyncEnabled: false })
  let savingSettings = $state(false)
  let loaded = $state(false)
  let error = $state('')
  let notice = $state('')
  let editing = $state(null) // null | 'new' | share object
  let dialog = $state(null)

  async function refresh() {
    try {
      ;[shares, users, volumes, settings] = await Promise.all([
        api.get('/api/shares'), api.get('/api/users'), api.get('/api/volumes'), api.get('/api/settings'),
      ])
      loaded = true
    } catch (e) {
      if (e.status === 401) onSessionLost()
      else error = e.message
    }
  }
  onMount(refresh)

  let mountedVolumes = $derived(volumes.filter((v) => v.mountpoint))

  async function save(req) {
    if (editing === 'new') await api.post('/api/shares', req)
    else await api.post(`/api/shares/${editing.name}`, req)
    notice = editing === 'new' ? `Share ${req.name} created.` : `Share ${editing.name} saved.`
    editing = null
    refresh()
  }

  async function toggle(key, value) {
    savingSettings = true
    error = ''
    try {
      await api.post('/api/settings', { ...settings, [key]: value })
      settings = { ...settings, [key]: value }
    } catch (e) {
      error = e.message
    } finally {
      savingSettings = false
    }
  }

  function askDelete(sh) {
    dialog = {
      title: `Stop sharing ${sh.name}?`,
      message: `The share disappears from the network. Its files are kept in ${sh.path}; create a share with the same name on the same volume to get them back.`,
      confirmLabel: 'Delete share',
      onConfirm: async () => {
        await api.post(`/api/shares/${sh.name}/delete`)
        dialog = null
        notice = `Share ${sh.name} deleted. Files kept in ${sh.path}.`
        refresh()
      },
    }
  }

  const accessLabel = { ro: 'read', rw: 'read & write' }
  function who(sh) {
    const parts = Object.entries(sh.access).sort().map(([u, a]) => `${u} (${accessLabel[a]})`)
    if (sh.guestRead) parts.push('guests (read)')
    return parts.length ? parts.join(', ') : 'nobody yet'
  }
</script>

<main class="page">
  <div class="page-head">
    <h1>Shares</h1>
    <span class="spacer"></span>
    {#if !editing}
      <button onclick={() => (editing = 'new')} disabled={!mountedVolumes.length}
        title={mountedVolumes.length ? '' : 'Create a volume first'}>New share</button>
    {/if}
  </div>

  {#if error}<p class="error">{error}</p>{/if}
  {#if notice}<p class="notice">{notice}</p>{/if}

  <div class="card list protocols">
    <div class="row">
      <label class="check">
        <input type="checkbox" checked={!settings.smbDisabled} disabled={savingSettings}
          onchange={(e) => toggle('smbDisabled', !e.currentTarget.checked)} />
        <span><strong>SMB</strong> <span class="muted small">· Windows, macOS, Linux and phones open shares over SMB</span></span>
      </label>
    </div>
    <div class="row">
      <label class="check">
        <input type="checkbox" checked={settings.rsyncEnabled} disabled={savingSettings}
          onchange={(e) => toggle('rsyncEnabled', e.currentTarget.checked)} />
        <span><strong>rsync</strong> <span class="muted small">· lets another machine back up shares that have "rsync access" (read only)</span></span>
      </label>
    </div>
  </div>

  {#if editing}
    <ShareForm rsyncEnabled={settings.rsyncEnabled} share={editing === 'new' ? null : editing} volumes={mountedVolumes} {users}
      onSave={save} onCancel={() => (editing = null)} />
  {/if}

  {#each shares as sh (sh.name)}
    <article class="card volume">
      <div class="volume-head">
        <h2>{sh.name}</h2>
        {#if !sh.available}<span class="badge bad">volume not mounted</span>{/if}
        {#if sh.timeMachine}<span class="badge neutral">Time Machine</span>{/if}
        {#if sh.recycleBin}<span class="badge neutral">Recycle bin</span>{/if}
        {#if sh.hidden}<span class="badge neutral">Hidden</span>{/if}
        <span class="spacer"></span>
        <button class="secondary" onclick={() => (editing = sh)} disabled={!!editing}>Edit</button>
        <button class="secondary danger-text" onclick={() => askDelete(sh)}>Delete</button>
      </div>
      {#if sh.description}<p class="muted">{sh.description}</p>{/if}
      <dl class="share-info">
        <dt>Access</dt><dd>{who(sh)}</dd>
        <dt>Folder</dt><dd class="mono">{sh.path}</dd>
        <dt>Windows</dt><dd class="mono">\\{hostname}\{sh.name}</dd>
        <dt>Mac / Linux</dt><dd class="mono">smb://{hostname}.local/{sh.name}</dd>
        {#if sh.rsyncRead}<dt>rsync</dt><dd class="mono">rsync://{hostname}.local/{sh.name}/{settings.rsyncEnabled ? '' : ' (rsync is off)'}</dd>{/if}
        {#if sh.allowedHosts?.length}<dt>Only from</dt><dd class="mono">{sh.allowedHosts.join(', ')}</dd>{/if}
      </dl>
    </article>
  {:else}
    {#if loaded && !editing}
      <div class="card empty">
        <p><strong>No shares yet.</strong></p>
        <p class="muted">A share is a folder on a volume that computers on your network can open, like a network drive.</p>
      </div>
    {/if}
  {/each}
</main>

{#if dialog}
  <ConfirmDialog {...dialog} onCancel={() => (dialog = null)} />
{/if}
