<script>
  import { onMount } from 'svelte'
  import { api } from '../api.js'
  import JobPanel from './JobPanel.svelte'
  import { t } from '../i18n.svelte.js'

  let info = $state(null)
  let job = $state(null)
  let error = $state('')

  async function load() {
    try {
      info = await api.get('/api/updates')
    } catch (e) {
      error = e.message
    }
  }
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
    {#if info.rebootRequired}<p class="warn-box">{t('An update needs a restart to take effect (usually the kernel).')}</p>{/if}
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
{/if}
