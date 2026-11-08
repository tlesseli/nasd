<script>
  import { onMount } from 'svelte'
  import { api } from '../api.js'

  // Network services of the NAS, switched on or off as a whole. Each share
  // then chooses which of them it uses (Shares page).
  let settings = $state(null)
  let status = $state({})
  let saving = $state(false)
  let error = $state('')

  async function load() {
    settings = await api.get('/api/settings')
    const s = await api.get('/api/services')
    status = Object.fromEntries(s.map((x) => [x.unit, x.active]))
  }
  onMount(load)

  async function toggle(key, value) {
    saving = true
    error = ''
    try {
      await api.post('/api/settings', { ...settings, [key]: value })
      await load()
    } catch (e) {
      error = e.message
    } finally {
      saving = false
    }
  }

  // inverted: the stored flag means "disabled".
  const services = [
    { key: 'smbDisabled', inverted: true, unit: 'smbd', label: 'SMB', help: 'File sharing for Windows, macOS, Linux and phones.' },
    { key: 'wsdDisabled', inverted: true, unit: 'wsdd2', label: 'Windows discovery', help: 'The NAS appears in Windows Explorer under Network (WS-Discovery).', needsSMB: true },
    { key: 'bonjourDisabled', inverted: true, unit: null, label: 'macOS discovery', help: 'The NAS appears in the Finder sidebar; Time Machine finds its shares (Bonjour).', needsSMB: true },
    { key: 'ftpEnabled', inverted: false, unit: 'vsftpd', label: 'FTP', help: 'For FTP clients (FileZilla, scanners, cameras). Users see the shares they have rights on.' },
    { key: 'rsyncEnabled', inverted: false, unit: 'rsync', label: 'rsync', help: 'Another NAS or computer can back up or fill the shares that allow rsync.' },
  ]
</script>

{#if error}<p class="error">{error}</p>{/if}

{#if settings}
  <div class="card list">
    {#each services as svc (svc.key)}
      {@const on = svc.inverted ? !settings[svc.key] : settings[svc.key]}
      <div class="row" class:sub={svc.needsSMB}>
        <label class="check">
          <input type="checkbox" checked={on} disabled={saving || (svc.needsSMB && settings.smbDisabled)}
            onchange={(e) => toggle(svc.key, svc.inverted ? !e.currentTarget.checked : e.currentTarget.checked)} />
          <span><strong>{svc.label}</strong> <span class="muted small">· {svc.help}</span></span>
        </label>
        <span class="spacer"></span>
        {#if svc.unit && on}
          <span class="badge {status[svc.unit] ? 'good' : 'bad'}">{status[svc.unit] ? 'Running' : 'Stopped'}</span>
        {/if}
      </div>
      {#if svc.key === 'ftpEnabled' && on}
        <div class="row sub">
          <label class="check">
            <input type="checkbox" checked={settings.ftpAllowPlain} disabled={saving}
              onchange={(e) => toggle('ftpAllowPlain', e.currentTarget.checked)} />
            <span><strong>Allow unencrypted FTP</strong> <span class="muted small">· only for old devices that don't support FTPS: passwords then travel readable on your network</span></span>
          </label>
        </div>
      {/if}
    {/each}
  </div>
  <p class="hint">Discovery announces the NAS itself. Each share chooses its own services and whether it's listed (Shares page).
    FTP uses the users' passwords: users created before FTP existed need a new password once (Users page) to log in over FTP.</p>
{/if}
