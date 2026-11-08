<script>
  import { onMount } from 'svelte'
  import { api } from '../api.js'
  import JobPanel from './JobPanel.svelte'

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
      <strong>{info.updates.length ? `${info.updates.length} updates available` : 'The system is up to date'}</strong>
      <span class="muted small">(as of the last check)</span>
      <span class="spacer"></span>
      <button class="secondary" onclick={() => start('/api/updates/check')} disabled={busy}>Check now</button>
      {#if info.updates.length}<button onclick={() => start('/api/updates/install')} disabled={busy}>Install updates</button>{/if}
    </div>
    {#if info.rebootRequired}<p class="warn-box">An update needs a restart to take effect (usually the kernel).</p>{/if}
    {#if info.updates.length}
      <table class="members">
        <thead><tr><th>Package</th><th>Installed</th><th>New</th></tr></thead>
        <tbody>
          {#each info.updates as u (u.package)}
            <tr><td class="mono">{u.package}</td><td class="mono small">{u.from}</td><td class="mono small">{u.to}</td></tr>
          {/each}
        </tbody>
      </table>
    {/if}
    <p class="hint">Updates come from Debian (security fixes and bug fixes). Your NAS settings are kept.</p>
  </div>
{/if}
