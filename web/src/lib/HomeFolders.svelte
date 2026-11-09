<script>
  import { onMount } from 'svelte'
  import { api } from '../api.js'
  import { t } from '../i18n.svelte.js'

  // Private home share per user, shown on the Shares page.
  let homes = $state(null)
  let volumes = $state([])
  let volume = $state('')
  let error = $state('')

  async function load() {
    const [h, v] = await Promise.all([api.get('/api/homes'), api.get('/api/volumes')])
    homes = h
    volumes = v.filter((x) => x.mountpoint && x.managed && !x.system)
    volume = homes.volume || volumes[0]?.name || ''
  }
  onMount(load)

  async function set(enabled) {
    error = ''
    try {
      await api.post('/api/homes', { enabled, volume })
      await load()
    } catch (e) {
      error = e.message
    }
  }
</script>

{#if homes}
  {@const parts = t('every user gets a private share named after them ({example}) that only they can open').split('{example}')}
  <div class="row">
    <label class="check">
      <input type="checkbox" checked={homes.enabled} disabled={!homes.enabled && !volume}
        onchange={(e) => set(e.currentTarget.checked)} />
      <span><strong>{t('Home folders')}</strong> <span class="muted small">· {parts[0]}<span class="mono">\\nas\alice</span>{parts[1] ?? ''}</span></span>
    </label>
    <span class="spacer"></span>
    {#if homes.enabled}
      <span class="muted small mono">{homes.path}</span>
    {:else if volumes.length > 1}
      <select bind:value={volume} aria-label={t('Volume for home folders')}>
        {#each volumes as v (v.name)}<option value={v.name}>{t('on {name}', { name: v.name })}</option>{/each}
      </select>
    {/if}
  </div>
  {#if error}<p class="error small">{error}</p>{/if}
{/if}
