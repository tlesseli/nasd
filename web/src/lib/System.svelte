<script>
  import { onMount } from 'svelte'
  import { api } from '../api.js'
  import PowerSettings from './PowerSettings.svelte'
  import NetworkSettings from './NetworkSettings.svelte'
  import NameTime from './NameTime.svelte'
  import Updates from './Updates.svelte'
  import ConfirmDialog from './ConfirmDialog.svelte'

  let { hostname, onSessionLost } = $props()
  let dialog = $state(null)

  function askPower(action) {
    const restart = action === 'reboot'
    dialog = {
      title: restart ? 'Restart the NAS?' : 'Shut down the NAS?',
      message: restart
        ? 'Shares are unavailable for a minute or two. Open transfers are interrupted.'
        : 'The NAS powers off. You will need to press its power button (or use Wake-on-LAN) to start it again.',
      confirmLabel: restart ? 'Restart' : 'Shut down',
      onConfirm: async () => {
        try {
          await api.post('/api/system/power', { action, force: false })
        } catch (e) {
          if (!e.message.includes('disk operation') || !window.confirm(e.message + '\n\nDo it anyway?')) throw e
          await api.post('/api/system/power', { action, force: true })
        }
        dialog = null
        notice = restart ? 'Restarting… this page reconnects when the NAS is back.' : 'Shutting down…'
      },
    }
  }

  let tab = $state('alerts')
  let error = $state('')

  // ---- alerts
  let kinds = $state([])
  let defaults = $state({})
  let cfg = $state(null)
  let emailsText = $state(['', '', ''])
  let saving = $state(false)
  let notice = $state('')
  let testMsg = $state(null)

  // ---- events & logs
  let events = $state([])
  let logSource = $state('all')
  let logSources = $state(['all'])
  let errorsOnly = $state(false)
  let logLines = $state([])
  let logEl = $state()

  async function load() {
    try {
      const a = await api.get('/api/alerts')
      kinds = a.kinds
      defaults = a.defaults
      cfg = a.settings
      emailsText = [0, 1, 2].map((i) => cfg.emails[i] ?? '')
      events = await api.get('/api/events')
    } catch (e) {
      if (e.status === 401) onSessionLost()
      else error = e.message
    }
  }
  onMount(load)

  function payload() {
    return { ...cfg, emails: emailsText.map((e) => e.trim()).filter(Boolean), smtp: { ...cfg.smtp, port: Number(cfg.smtp.port) || 0 } }
  }

  async function save(event) {
    event.preventDefault()
    saving = true
    error = notice = ''
    try {
      await api.post('/api/alerts', payload())
      notice = 'Alert settings saved.'
      load()
    } catch (e) {
      error = e.message
    } finally {
      saving = false
    }
  }

  async function test() {
    testMsg = { ok: null, text: 'Sending…' }
    try {
      await api.post('/api/alerts/test', payload())
      testMsg = { ok: true, text: 'Test message sent. Check the inbox (and the spam folder).' }
    } catch (e) {
      testMsg = { ok: false, text: e.message }
    }
  }

  function toggleKind(id, on) {
    cfg.disabled = on ? cfg.disabled.filter((d) => d !== id) : [...cfg.disabled, id]
  }

  // Common providers, to save looking them up.
  const providers = {
    Gmail: { host: 'smtp.gmail.com', port: 587, security: 'starttls' },
    'Outlook / Hotmail': { host: 'smtp-mail.outlook.com', port: 587, security: 'starttls' },
    'Yahoo': { host: 'smtp.mail.yahoo.com', port: 465, security: 'tls' },
    'iCloud': { host: 'smtp.mail.me.com', port: 587, security: 'starttls' },
  }
  function useProvider(name) {
    if (providers[name]) cfg.smtp = { ...cfg.smtp, ...providers[name] }
  }

  async function loadLogs() {
    try {
      const r = await api.get(`/api/logs?source=${logSource}&lines=300&errors=${errorsOnly ? 1 : 0}`)
      logLines = r.lines
      logSources = r.sources
      queueMicrotask(() => logEl && (logEl.scrollTop = logEl.scrollHeight))
    } catch (e) {
      error = e.message
    }
  }
  $effect(() => {
    if (tab === 'logs') loadLogs()
    if (tab === 'events') api.get('/api/events').then((e) => (events = e)).catch(() => {})
  })

  const sevClass = { critical: 'bad', warning: 'warn', info: 'neutral' }
</script>

<main class="page">
  <div class="page-head">
    <h1>System</h1>
    <span class="spacer"></span>
    <button class="secondary" onclick={() => askPower('reboot')}>Restart</button>
    <button class="secondary danger-text" onclick={() => askPower('poweroff')}>Shut down</button>
  </div>
  <nav class="tabs" aria-label="System sections">
    {#each [['alerts', 'Alerts'], ['power', 'Power'], ['network', 'Network'], ['nametime', 'Name & time'], ['updates', 'Updates'], ['events', 'Events'], ['logs', 'Logs']] as [id, label]}
      <button class:active={tab === id} onclick={() => (tab = id)}>{label}</button>
    {/each}
  </nav>

  {#if error}<p class="error">{error}</p>{/if}
  {#if notice}<p class="notice">{notice}</p>{/if}

  {#if tab === 'alerts' && cfg}
    <form class="card create" onsubmit={save}>
      <fieldset>
        <legend>Send alerts to</legend>
        {#each emailsText as _, i}
          <input type="email" bind:value={emailsText[i]} placeholder={i === 0 ? 'you@example.com' : 'optional'} aria-label="Email {i + 1}" />
        {/each}
        <p class="hint">Up to three addresses. An address you read on your phone helps when you're away.</p>
      </fieldset>

      <fieldset>
        <legend>Outgoing mail server (SMTP)</legend>
        <label>
          Provider
          <select onchange={(e) => useProvider(e.currentTarget.value)}>
            <option value="">Other / fill in below</option>
            {#each Object.keys(providers) as p}<option>{p}</option>{/each}
          </select>
        </label>
        <div class="two-cols">
          <label>Server <input bind:value={cfg.smtp.host} placeholder="smtp.example.com" spellcheck="false" /></label>
          <label>Port <input bind:value={cfg.smtp.port} inputmode="numeric" placeholder="587" /></label>
          <label>
            Security
            <select bind:value={cfg.smtp.security}>
              <option value="starttls">STARTTLS (usually port 587)</option>
              <option value="tls">SSL/TLS (usually port 465)</option>
              <option value="none">None (local server only)</option>
            </select>
          </label>
        </div>
        <div class="two-cols">
          <label>User <input bind:value={cfg.smtp.user} autocomplete="off" spellcheck="false" /></label>
          <label>Password <span class="hint">(empty = keep the saved one)</span>
            <input type="password" bind:value={cfg.smtp.password} autocomplete="new-password" /></label>
          <label>Sender <span class="hint">(optional)</span> <input bind:value={cfg.smtp.from} placeholder="nas@example.com" /></label>
        </div>
        <p class="hint">Gmail and Outlook need an <em>app password</em> (created in your account's security settings), not your normal password.</p>
        <div class="add">
          <button type="button" class="secondary" onclick={test}>Send test message</button>
          {#if testMsg}<span class={testMsg.ok === false ? 'error small' : testMsg.ok ? 'good-text small' : 'muted small'}>{testMsg.text}</span>{/if}
        </div>
      </fieldset>

      <fieldset>
        <legend>Events</legend>
        <table class="members access">
          <tbody>
            {#each kinds as k (k.id)}
              <tr>
                <td>
                  <label class="check">
                    <input type="checkbox" checked={k.mandatory || !cfg.disabled.includes(k.id)} disabled={k.mandatory}
                      onchange={(e) => toggleKind(k.id, e.currentTarget.checked)} />
                    <span>{k.label}</span>
                  </label>
                </td>
                <td class="right">
                  <span class="badge {sevClass[k.severity]}">{k.severity}</span>
                  {#if k.mandatory}<span class="muted small">always</span>{/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
        <div class="two-cols">
          <label>Disk temperature alert above (°C)
            <input type="number" min="30" max="80" bind:value={cfg.diskTempLimit} placeholder={String(defaults.diskTempLimit)} /></label>
          <label>Volume full alert above (%)
            <input type="number" min="50" max="99" bind:value={cfg.volumeFullPercent} placeholder={String(defaults.volumeFullPercent)} /></label>
        </div>
      </fieldset>

      <div class="actions"><button type="submit" disabled={saving}>{saving ? 'Saving…' : 'Save'}</button></div>
    </form>
  {/if}

  {#if tab === 'power'}<PowerSettings />{/if}
  {#if tab === 'network'}<NetworkSettings />{/if}
  {#if tab === 'nametime'}<NameTime {hostname} />{/if}
  {#if tab === 'updates'}<Updates />{/if}

  {#if tab === 'events'}
    <div class="card list">
      {#each events as e, i (i)}
        <div class="row event">
          <span class="badge {sevClass[e.severity]}">{e.severity}</span>
          <strong>{e.subject}</strong>
          <span class="spacer"></span>
          <span class="muted small" title={e.emailError || ''}>
            {new Date(e.time).toLocaleString()}{e.emailed ? ' · emailed' : e.emailError ? ' · email failed' : ''}
          </span>
          {#if e.body}<p class="muted small event-body">{e.body}</p>{/if}
        </div>
      {:else}
        <p class="muted">No events yet.</p>
      {/each}
    </div>
  {/if}

  {#if tab === 'logs'}
    <div class="add">
      <select bind:value={logSource} onchange={loadLogs} aria-label="Log source">
        {#each logSources as s}<option>{s}</option>{/each}
      </select>
      <label class="check"><input type="checkbox" bind:checked={errorsOnly} onchange={loadLogs} /><span>Warnings and errors only</span></label>
      <button class="secondary" onclick={loadLogs}>Refresh</button>
    </div>
    <pre class="logs" bind:this={logEl}>{logLines.join('\n')}</pre>
  {/if}
</main>

{#if dialog}<ConfirmDialog {...dialog} onCancel={() => (dialog = null)} />{/if}
