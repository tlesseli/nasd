<script>
  import { onMount } from 'svelte'
  import { api } from '../api.js'
  import { t } from '../i18n.svelte.js'

  // The public folder (\\nas\public): listed when browsing the NAS, opens
  // without a password. Next to the home folders on the Shares page.
  let pub = $state(null)
  let volumes = $state([])
  let volume = $state('')
  let busy = $state(false)
  let error = $state('')

  async function load() {
    const [p, v] = await Promise.all([api.get('/api/public'), api.get('/api/volumes')])
    pub = p
    volumes = v.filter((x) => x.mountpoint && x.managed && !x.system)
    volume = pub.volume || volumes[0]?.name || ''
  }
  onMount(load)

  async function set(enabled, writable) {
    error = ''
    busy = true
    try {
      await api.post('/api/public', { enabled, volume, writable })
      await load()
    } catch (e) {
      error = e.message
    } finally {
      busy = false
    }
  }
</script>

{#if pub}
  <div class="row">
    <label class="check">
      <input type="checkbox" checked={pub.enabled} disabled={busy || (!pub.enabled && !volume)}
        onchange={(e) => set(e.currentTarget.checked, pub.writable)} />
      <span><strong>{t('Public folder')}</strong>
        <span class="muted small">· {t('listed on the network, opens without a password')} (<span class="mono">\\nas\public</span>)</span></span>
    </label>
    <span class="spacer"></span>
    {#if busy}<span class="spinner" aria-label={t('Please wait…')}></span>{/if}
    {#if !pub.enabled && volumes.length > 1}
      <select bind:value={volume} aria-label={t('Volume for the public folder')}>
        {#each volumes as v (v.name)}<option value={v.name}>{t('on {name}', { name: v.name })}</option>{/each}
      </select>
    {/if}
  </div>
  {#if pub.enabled}
    <label class="check sub">
      <input type="checkbox" checked={pub.writable} disabled={busy} onchange={(e) => set(true, e.currentTarget.checked)} />
      <span>{t('Guests can also add and change files')}
        <span class="muted small">· {t('otherwise they can only read; users with an account can always write')}</span></span>
    </label>
    <div class="muted small mono">{pub.path}</div>
  {/if}
  {#if error}<p class="error small">{error}</p>{/if}
{/if}

<style>
  .row { display: flex; align-items: center; gap: 0.6rem; flex-wrap: wrap; }
  .sub { margin: 0.6rem 0 0.3rem 1.6rem; }
</style>
