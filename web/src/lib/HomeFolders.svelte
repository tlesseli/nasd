<script>
  import { onMount } from 'svelte'
  import { api } from '../api.js'

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
  <div class="row">
    <label class="check">
      <input type="checkbox" checked={homes.enabled} disabled={!homes.enabled && !volume}
        onchange={(e) => set(e.currentTarget.checked)} />
      <span><strong>Home folders</strong> <span class="muted small">· every user gets a private share named after them
        (<span class="mono">\\nas\alice</span>) that only they can open</span></span>
    </label>
    <span class="spacer"></span>
    {#if homes.enabled}
      <span class="muted small mono">{homes.path}</span>
    {:else if volumes.length > 1}
      <select bind:value={volume} aria-label="Volume for home folders">
        {#each volumes as v (v.name)}<option value={v.name}>on {v.name}</option>{/each}
      </select>
    {/if}
  </div>
  {#if error}<p class="error small">{error}</p>{/if}
{/if}
