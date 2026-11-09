<script>
  import { onMount } from 'svelte'
  import { api } from '../api.js'
  import PowerSettings from './PowerSettings.svelte'
  import NetworkSettings from './NetworkSettings.svelte'
  import GeneralSettings from './GeneralSettings.svelte'
  import Icon from '../ui/Icon.svelte'
  import Updates from './Updates.svelte'
  import HardwareSettings from './HardwareSettings.svelte'
  import ServicesSettings from './ServicesSettings.svelte'
  import ConfirmDialog from './ConfirmDialog.svelte'
  import About from './About.svelte'
  import { t, N_ } from '../i18n.svelte.js'

  let { hostname, onSessionLost, sectionId = 'general' } = $props()

  // Sub-sections, each with its own URL (#/system/alerts...).
  const sections = [
    ['general', N_('General'), 'server'], ['network', N_('Network'), 'network'], ['services', N_('Services'), 'folder'],
    ['alerts', N_('Alerts'), 'bell'], ['power', N_('Power & UPS'), 'power'], ['hardware', N_('Hardware'), 'drive'],
    ['updates', N_('Software update'), 'refresh'], ['events', N_('Events & logs'), 'file'], ['restart', N_('Restart / shut down'), 'power'],
    ['about', N_('About'), 'info'],
  ]
  let dialog = $state(null)

  function askPower(action) {
    const restart = action === 'reboot'
    dialog = {
      title: restart ? t('Restart the NAS?') : t('Shut down the NAS?'),
      message: restart
        ? t('Shares are unavailable for a minute or two. Open transfers are interrupted.')
        : t('The NAS powers off. You will need to press its power button (or use Wake-on-LAN) to start it again.'),
      confirmLabel: restart ? t('Restart') : t('Shut down'),
      onConfirm: async () => {
        try {
          await api.post('/api/system/power', { action, force: false })
        } catch (e) {
          if (!e.message.includes('disk operation') || !window.confirm(e.message + '\n\n' + t('Do it anyway?'))) throw e
          await api.post('/api/system/power', { action, force: true })
        }
        dialog = null
        notice = restart ? t('Restarting… this page reconnects when the NAS is back.') : t('Shutting down…')
      },
    }
  }

  const tab = $derived(sections.some(([id]) => id === sectionId) ? sectionId : 'general')
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
      notice = t('Alert settings saved.')
      load()
    } catch (e) {
      error = e.message
    } finally {
      saving = false
    }
  }

  async function test() {
    testMsg = { ok: null, text: t('Sending…') }
    try {
      await api.post('/api/alerts/test', payload())
      testMsg = { ok: true, text: t('Test message sent. Check the inbox (and the spam folder).') }
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

  // Events or logs, emptied after the admin password (checked by the NAS).
  function askClear(clearEvents, clearLogs) {
    dialog = {
      title: clearEvents ? t('Clear all events?') : t('Clear all logs?'),
      message: clearEvents
        ? t('The event list is emptied. Alerts already emailed are not affected.')
        : t('The system logs (nasd, Samba, kernel…) are deleted. They help to understand a problem: clear them only if you no longer need them.'),
      confirmLabel: clearEvents ? t('Clear events') : t('Clear logs'),
      askPassword: true,
      onConfirm: async (password) => {
        await api.post('/api/events/clear', { events: clearEvents, logs: clearLogs, password })
        dialog = null
        // The "cleared" record is written once its alert email went out.
        events = await api.get('/api/events')
        setTimeout(async () => (events = await api.get('/api/events').catch(() => events)), 2500)
        if (clearLogs) loadLogs()
      },
    }
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
    if (tab === 'events') {
      loadLogs()
      api.get('/api/events').then((e) => (events = e)).catch(() => {})
    }
  })

  const sevClass = { critical: 'bad', warning: 'warn', info: 'neutral' }
</script>

<main class="page">
  <div class="page-head">
    <div><div class="eyebrow">{t('Device-wide configuration')}</div><h1>{t('System Settings')}</h1></div>
  </div>

  <div class="settings">
    <nav class="subnav" aria-label={t('Settings sections')}>
      {#each sections as [id, label, icon] (id)}
        <button class:active={tab === id} aria-current={tab === id ? 'page' : undefined} onclick={() => (location.hash = `/system/${id}`)}>
          <Icon name={icon} size={15} /> {t(label)}
        </button>
      {/each}
    </nav>
    <div class="stack settings-body">
  {#if error}<p class="error">{error}</p>{/if}
  {#if notice}<p class="notice">{notice}</p>{/if}

  {#if tab === 'alerts' && cfg}
    <form class="card create" onsubmit={save}>
      <fieldset>
        <legend>{t('Send alerts to')}</legend>
        {#each emailsText as _, i}
          <input type="email" bind:value={emailsText[i]} placeholder={i === 0 ? 'you@example.com' : t('optional')} aria-label={t('Email {n}', { n: i + 1 })} />
        {/each}
        <p class="hint">{t("Up to three addresses. An address you read on your phone helps when you're away.")}</p>
      </fieldset>

      <fieldset>
        <legend>{t('Outgoing mail server (SMTP)')}</legend>
        <label>
          {t('Provider')}
          <select onchange={(e) => useProvider(e.currentTarget.value)}>
            <option value="">{t('Other / fill in below')}</option>
            {#each Object.keys(providers) as p}<option>{p}</option>{/each}
          </select>
        </label>
        <div class="two-cols">
          <label>{t('Server')} <input bind:value={cfg.smtp.host} placeholder="smtp.example.com" spellcheck="false" /></label>
          <label>{t('Port')} <input bind:value={cfg.smtp.port} inputmode="numeric" placeholder="587" /></label>
          <label>
            {t('Security')}
            <select bind:value={cfg.smtp.security}>
              <option value="starttls">{t('STARTTLS (usually port 587)')}</option>
              <option value="tls">{t('SSL/TLS (usually port 465)')}</option>
              <option value="none">{t('None (local server only)')}</option>
            </select>
          </label>
        </div>
        <div class="two-cols">
          <label>{t('User')} <input bind:value={cfg.smtp.user} autocomplete="off" spellcheck="false" /></label>
          <label>{t('Password')} <span class="hint">{t('(empty = keep the saved one)')}</span>
            <input type="password" bind:value={cfg.smtp.password} autocomplete="new-password" /></label>
          <label>{t('Sender')} <span class="hint">{t('(optional)')}</span> <input bind:value={cfg.smtp.from} placeholder="nas@example.com" /></label>
        </div>
        <p class="hint">{t('Gmail and Outlook need an')} <em>{t('app password')}</em> {t("(created in your account's security settings), not your normal password.")}</p>
        <div class="add">
          <button type="button" class="secondary" onclick={test}>{t('Send test message')}</button>
          {#if testMsg}<span class={testMsg.ok === false ? 'error small' : testMsg.ok ? 'good-text small' : 'muted small'}>{testMsg.text}</span>{/if}
        </div>
      </fieldset>

      <fieldset>
        <legend>{t('Events')}</legend>
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
                  {#if k.mandatory}<span class="muted small">{t('always')}</span>{/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
        <div class="two-cols">
          <label>{t('Disk temperature alert above (°C)')}
            <input type="number" min="30" max="80" bind:value={cfg.diskTempLimit} placeholder={String(defaults.diskTempLimit)} /></label>
          <label>{t('Volume full alert above (%)')}
            <input type="number" min="50" max="99" bind:value={cfg.volumeFullPercent} placeholder={String(defaults.volumeFullPercent)} /></label>
        </div>
      </fieldset>

      <div class="actions"><button type="submit" disabled={saving}>{saving ? t('Saving…') : t('Save')}</button></div>
    </form>
  {/if}

  {#if tab === 'services'}<ServicesSettings />{/if}
  {#if tab === 'power'}<PowerSettings />{/if}
  {#if tab === 'network'}<NetworkSettings />{/if}
  {#if tab === 'general'}<GeneralSettings {hostname} />{/if}
  {#if tab === 'restart'}
    <section class="card danger-zone">
      <div class="card-head"><h2>{t('Restart or shut down')}</h2></div>
      <p class="small muted">{t("Connected computers lose their shares while the NAS is down. Running disk operations are protected: you'll be asked before interrupting one.")}</p>
      <div class="actions">
        <button class="secondary" onclick={() => askPower('reboot')}>{t('Restart')}</button>
        <button class="danger" onclick={() => askPower('poweroff')}>{t('Shut down')}</button>
      </div>
    </section>
  {/if}
  {#if tab === 'updates'}<Updates />{/if}
  {#if tab === 'hardware'}<HardwareSettings />{/if}

  {#if tab === 'about'}<About {hostname} />{/if}

  {#if tab === 'events'}
    <div class="add clear-bar">
      <span class="muted small">{t('Clearing needs the admin password. A single event then records it.')}</span>
      <span class="spacer"></span>
      <button class="secondary" onclick={() => askClear(true, false)}>{t('Clear events')}</button>
    </div>
    <div class="card list">
      {#each events as e, i (i)}
        <div class="row event">
          <span class="badge {sevClass[e.severity]}">{e.severity}</span>
          <strong>{e.subject}</strong>
          <span class="spacer"></span>
          <span class="muted small" title={e.emailError || ''}>
            {new Date(e.time).toLocaleString()}{e.emailed ? ' · ' + t('emailed') : e.emailError ? ' · ' + t('email failed') : ''}
          </span>
          {#if e.body}<p class="muted small event-body">{e.body}</p>{/if}
        </div>
      {:else}
        <p class="muted">{t('No events yet.')}</p>
      {/each}
    </div>
  {/if}

  {#if tab === 'events'}
    <h2 class="logs-title">{t('Logs')}</h2>
    <div class="add">
      <select bind:value={logSource} onchange={loadLogs} aria-label={t('Log source')}>
        {#each logSources as s}<option>{s}</option>{/each}
      </select>
      <label class="check"><input type="checkbox" bind:checked={errorsOnly} onchange={loadLogs} /><span>{t('Warnings and errors only')}</span></label>
      <button class="secondary" onclick={loadLogs}>{t('Refresh')}</button>
      <button class="secondary" onclick={() => askClear(false, true)}>{t('Clear logs')}</button>
    </div>
    <pre class="logs" bind:this={logEl}>{logLines.join('\n')}</pre>
  {/if}
    </div>
  </div>
</main>

{#if dialog}<ConfirmDialog {...dialog} onCancel={() => (dialog = null)} />{/if}
