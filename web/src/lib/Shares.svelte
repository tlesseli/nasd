<script>
  import { onMount } from 'svelte'
  import { api } from '../api.js'
  import ShareForm from './ShareForm.svelte'
  import ConfirmDialog from './ConfirmDialog.svelte'
  import HomeFolders from './HomeFolders.svelte'

  let { hostname, onSessionLost } = $props()

  let shares = $state([])
  let users = $state([])
  let volumes = $state([])
  let settings = $state({ smbDisabled: false, rsyncEnabled: false, wsdDisabled: false, bonjourDisabled: false })
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
    {#each [
      { key: 'smbDisabled', inverted: true, label: 'SMB', help: 'file sharing for Windows, macOS, Linux and phones' },
      { key: 'wsdDisabled', inverted: true, label: 'Windows discovery', help: 'the NAS appears in Windows Explorer under Network (wsdd2)', needsSMB: true },
      { key: 'bonjourDisabled', inverted: true, label: 'macOS discovery', help: 'the NAS appears in the Finder sidebar, Time Machine finds its shares (Bonjour)', needsSMB: true },
      { key: 'rsyncEnabled', inverted: false, label: 'rsync', help: 'another machine can back up the shares that allow rsync (read only)' },
    ] as p (p.key)}
      {@const on = p.inverted ? !settings[p.key] : settings[p.key]}
      <div class="row" class:sub={p.needsSMB}>
        <label class="check">
          <input type="checkbox" checked={on} disabled={savingSettings || (p.needsSMB && settings.smbDisabled)}
            onchange={(e) => toggle(p.key, p.inverted ? !e.currentTarget.checked : e.currentTarget.checked)} />
          <span><strong>{p.label}</strong> <span class="muted small">· {p.help}</span></span>
        </label>
      </div>
    {/each}
    <p class="hint">Discovery announces the NAS itself; which shares are listed is chosen per share. Each share also chooses its services (SMB, rsync).</p>
    {#if loaded}<HomeFolders />{/if}
  </div>

  {#if editing}
    <ShareForm {settings} share={editing === 'new' ? null : editing} volumes={mountedVolumes} {users}
      onSave={save} onCancel={() => (editing = null)} />
  {/if}

  {#if shares.length}
    <div class="card table-card">
      <table class="members shares-table">
        <thead>
          <tr><th>Share</th><th>Volume</th><th>Access</th><th>Services</th><th>Options</th><th></th></tr>
        </thead>
        <tbody>
          {#each shares as sh (sh.name)}
            <tr>
              <td>
                <strong>{sh.name}</strong>
                {#if !sh.available}<span class="badge bad">not mounted</span>{/if}
                {#if sh.description}<div class="muted small">{sh.description}</div>{/if}
                <div class="addresses">
                  {#if !sh.smbDisabled}
                    <span class="mono small" title="Windows">\\{hostname}\{sh.name}</span>
                    <span class="mono small" title="Mac / Linux">smb://{hostname}.local/{sh.name}</span>
                  {/if}
                  {#if sh.rsyncRead}<span class="mono small" title="rsync">rsync://{hostname}.local/{sh.name}/</span>{/if}
                </div>
              </td>
              <td class="small">{sh.volume}</td>
              <td class="small">
                {who(sh)}
                {#if sh.allowedHosts?.length}<div class="muted">only from {sh.allowedHosts.join(', ')}</div>{/if}
              </td>
              <td>
                <div class="badges">
                  {#if !sh.smbDisabled}<span class="badge info" class:off={settings.smbDisabled} title={settings.smbDisabled ? 'SMB service is off' : ''}>SMB</span>{/if}
                  {#if sh.rsyncRead}<span class="badge info" class:off={!settings.rsyncEnabled} title={settings.rsyncEnabled ? '' : 'rsync service is off'}>rsync</span>{/if}
                  {#if sh.smbDisabled && !sh.rsyncRead}<span class="muted small">none</span>{/if}
                </div>
              </td>
              <td>
                <div class="badges">
                  {#if !sh.smbDisabled && sh.hidden}<span class="badge neutral">Not listed</span>{/if}
                  {#if sh.timeMachine}<span class="badge neutral">Time Machine</span>{/if}
                  {#if sh.recycleBin}<span class="badge neutral">Recycle bin</span>{/if}
                  {#if sh.guestRead}<span class="badge neutral">Guests</span>{/if}
                </div>
              </td>
              <td class="right row-actions">
                <button class="link" onclick={() => (editing = sh)} disabled={!!editing}>Edit</button>
                <button class="link danger-text" onclick={() => askDelete(sh)}>Delete</button>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {:else if loaded && !editing}
    <div class="card empty">
      <p><strong>No shares yet.</strong></p>
      <p class="muted">A share is a folder on a volume that computers on your network can open, like a network drive.</p>
    </div>
  {/if}
</main>

{#if dialog}
  <ConfirmDialog {...dialog} onCancel={() => (dialog = null)} />
{/if}
