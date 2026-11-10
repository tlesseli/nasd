<script>
  import { onMount } from 'svelte'
  import { api } from '../api.js'
  import JobPanel from './JobPanel.svelte'
  import { t } from '../i18n.svelte.js'

  let info = $state(null)
  let job = $state(null)
  let error = $state('')
  let auto = $state(null) // automatic updates form
  let rebootTime = $state('04:00')
  let saved = $state(null) // {ok, text}

  async function load() {
    try {
      info = await api.get('/api/updates')
      if (!auto) {
        auto = { ...info.auto }
        if (auto.autoReboot) rebootTime = `${String(auto.rebootHour).padStart(2, '0')}:${String(auto.rebootMinute).padStart(2, '0')}`
      }
    } catch (e) {
      error = e.message
    }
  }

  async function saveAuto() {
    const [h, m] = rebootTime.split(':').map(Number)
    try {
      await api.post('/api/updates/auto', { ...auto, rebootHour: h || 0, rebootMinute: m || 0 })
      saved = { ok: true, text: t('Saved.') }
    } catch (e) {
      saved = { ok: false, text: e.message }
    }
  }
  const when = (d) => new Date(d).toLocaleString([], { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' })
  onMount(load)

  async function start(path) {
    error = ''
    try {
      job = await api.post(path)
    } catch (e) {
      error = e.message
    }
  }
  let busy = $derived(job?.status === 'running')
</script>

{#if error}<p class="error">{error}</p>{/if}
{#if job}<JobPanel {job} onFinished={load} onClose={() => (job = null)} />{/if}

{#if info}
  <div class="card create">
    <div class="add">
      <strong>{info.updates.length ? (info.updates.length === 1 ? t('{n} update available', { n: info.updates.length }) : t('{n} updates available', { n: info.updates.length })) : t('The system is up to date')}</strong>
      <span class="muted small">{t('(as of the last check)')}</span>
      <span class="spacer"></span>
      <button class="secondary" onclick={() => start('/api/updates/check')} disabled={busy}>{t('Check now')}</button>
      {#if info.updates.length}<button onclick={() => start('/api/updates/install')} disabled={busy}>{t('Install updates')}</button>{/if}
    </div>
    {#if info.rebootRequired}<p class="warn-box">{t('An update needs a restart to take effect (usually the kernel).')}{#if info.rebootPackages?.length} <span class="mono small">{info.rebootPackages.join(', ')}</span>{/if}</p>{/if}
    {#if info.updates.length}
      <table class="members">
        <thead><tr><th>{t('Package')}</th><th>{t('Installed')}</th><th>{t('New')}</th></tr></thead>
        <tbody>
          {#each info.updates as u (u.package)}
            <tr><td class="mono">{u.package}</td><td class="mono small">{u.from}</td><td class="mono small">{u.to}</td></tr>
          {/each}
        </tbody>
      </table>
    {/if}
    <p class="hint">{t('Updates come from Debian (security fixes and bug fixes). Your NAS settings are kept.')}</p>
  </div>

  {#if auto}
    <div class="card create">
      <h2>{t('Automatic updates')}</h2>
      <label class="check"><input type="checkbox" checked={!auto.autoDisabled} onchange={(e) => (auto.autoDisabled = !e.currentTarget.checked)} />
        <span>{t('Install security updates automatically (checked every day)')}</span></label>
      {#if !auto.autoDisabled}
        <div class="add">
          <label class="check"><input type="checkbox" bind:checked={auto.autoReboot} /><span>{t('Restart by itself when an update needs it, at')}</span></label>
          <input type="time" bind:value={rebootTime} disabled={!auto.autoReboot} aria-label={t('Restart time')} />
        </div>
      {/if}
      <div class="add">
        <button class="secondary" onclick={saveAuto}>{t('Save')}</button>
        {#if saved}<span class={saved.ok ? 'good-text small' : 'error small'}>{saved.text}</span>{/if}
      </div>
      <p class="hint">{t('Debian security fixes (Samba, SSH, the kernel...) are installed without you, and you get an event listing them. A new kernel only takes effect after a restart: without automatic restart, the NAS tells you when one is needed.')}</p>
      <p class="muted small">{info.lastAutoRun ? t('Last automatic check: {date}', { date: when(info.lastAutoRun) }) : t('No automatic check has run yet.')}</p>
      {#if info.autoRuns.length}
        <table class="members">
          <thead><tr><th>{t('Date')}</th><th>{t('Packages')}</th><th></th></tr></thead>
          <tbody>
            {#each info.autoRuns as r (r.time)}
              <tr><td class="small">{when(r.time)}</td><td class="mono small">{r.packages.join(', ')}</td>
                <td><span class="badge {r.ok ? 'good' : 'bad'}" title={r.error || ''}>{r.ok ? t('Installed') : t('Failed')}</span></td></tr>
            {/each}
          </tbody>
        </table>
      {/if}
    </div>
  {/if}
{/if}
