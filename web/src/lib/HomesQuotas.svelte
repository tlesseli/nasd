<script>
  import { onMount } from 'svelte'
  import { api, formatBytes } from '../api.js'
  import JobPanel from './JobPanel.svelte'

  // Home folders and disk quotas, shown on the Users page.
  let { users, onChanged } = $props()

  let homes = $state(null)
  let homeVolume = $state('')
  let quotas = $state([])
  let job = $state(null)
  let msg = $state(null)
  let editing = $state(null) // {name, value, unit}

  async function load() {
    ;[homes, quotas] = await Promise.all([api.get('/api/homes'), api.get('/api/quotas')])
    homeVolume = homes.volume || quotas[0]?.volume || ''
  }
  onMount(load)

  async function setHomes(enabled) {
    msg = null
    try {
      await api.post('/api/homes', { enabled, volume: homeVolume })
      await load()
    } catch (e) {
      msg = { ok: false, text: e.message }
    }
  }

  async function enableQuotas(volume) {
    msg = null
    try {
      job = await api.post(`/api/volumes/${volume}/quotas`)
    } catch (e) {
      msg = { ok: false, text: e.message }
    }
  }

  function editQuota(u) {
    const gb = u.quotaMB >= 1024 && u.quotaMB % 1024 === 0
    editing = { name: u.name, value: u.quotaMB ? (gb ? u.quotaMB / 1024 : u.quotaMB) : '', unit: gb || !u.quotaMB ? 'GB' : 'MB' }
  }

  async function saveQuota() {
    const mb = Math.round((Number(editing.value) || 0) * (editing.unit === 'GB' ? 1024 : 1))
    try {
      await api.post(`/api/users/${editing.name}/quota`, { quotaMB: mb })
      editing = null
      onChanged()
      load()
    } catch (e) {
      msg = { ok: false, text: e.message }
    }
  }

  const quotaText = (mb) => (mb ? (mb % 1024 === 0 ? `${mb / 1024} GB` : `${mb} MB`) : 'Unlimited')
  const usageOf = (name) => quotas.filter((q) => q.enabled).map((q) => ({ volume: q.volume, ...(q.usage.find((u) => u.user === name) ?? { usedKB: 0, limitKB: 0 }) }))
  const anyQuotas = $derived(quotas.some((q) => q.enabled))
</script>

{#if job}<JobPanel {job} onFinished={load} onClose={() => (job = null)} />{/if}
{#if msg}<p class={msg.ok ? 'notice' : 'error'}>{msg.text}</p>{/if}

{#if homes}
  <h2>Home folders</h2>
  <div class="card create">
    <p class="muted small">Each user gets a private share named after them (<span class="mono">\\nas\alice</span>), visible only to them.</p>
    <div class="add">
      {#if homes.enabled}
        <span class="badge good">On</span>
        <span class="small">Folders in <span class="mono">{homes.path}</span></span>
        <span class="spacer"></span>
        <button class="secondary" onclick={() => setHomes(false)}>Turn off</button>
      {:else}
        <select bind:value={homeVolume} aria-label="Volume for home folders">
          {#each quotas as q (q.volume)}<option value={q.volume}>{q.volume}</option>{/each}
        </select>
        <button onclick={() => setHomes(true)} disabled={!homeVolume}>Turn on</button>
        {#if homes.path}<span class="muted small">Existing folders in {homes.path} are kept and reused.</span>{/if}
      {/if}
    </div>
  </div>

  <h2>Disk quotas</h2>
  <div class="card create">
    <p class="muted small">A quota limits the space a user can fill on each volume. It applies to everything they save, in any share.</p>
    {#each quotas as q (q.volume)}
      <div class="add">
        <strong>{q.volume}</strong>
        {#if q.enabled}<span class="badge good">Quotas on</span>
        {:else if q.filesystem === 'ext4'}
          <span class="muted small">Quotas off</span>
          <button class="secondary" onclick={() => enableQuotas(q.volume)} disabled={job?.status === 'running'}>Enable quotas</button>
          <span class="hint">The volume is unmounted for a few seconds: nobody should be using it.</span>
        {:else}<span class="muted small">Not available on {q.filesystem} (per-user quotas need ext4)</span>{/if}
      </div>
    {/each}

    {#if users.length}
      <table class="members">
        <thead><tr><th>User</th><th>Quota</th>{#if anyQuotas}<th>Used</th>{/if}<th></th></tr></thead>
        <tbody>
          {#each users as u (u.name)}
            <tr>
              <td><strong>{u.name}</strong></td>
              <td>
                {#if editing?.name === u.name}
                  <span class="add">
                    <input type="number" min="0" bind:value={editing.value} placeholder="0 = unlimited" style="width: 8rem" />
                    <select bind:value={editing.unit} aria-label="Unit"><option>GB</option><option>MB</option></select>
                    <button class="secondary" onclick={saveQuota}>Save</button>
                  </span>
                {:else}{quotaText(u.quotaMB)}{/if}
              </td>
              {#if anyQuotas}
                <td class="small">
                  {#each usageOf(u.name) as x}
                    {@const pct = x.limitKB ? Math.round((100 * x.usedKB) / x.limitKB) : 0}
                    <div>{x.volume}: {formatBytes(x.usedKB * 1024)}{x.limitKB ? ` (${pct}%)` : ''}
                      {#if x.limitKB}<span class="meter"><span style="width: {Math.min(pct, 100)}%" class:full={pct >= 90}></span></span>{/if}
                    </div>
                  {/each}
                </td>
              {/if}
              <td class="right">{#if editing?.name !== u.name}<button class="link" onclick={() => editQuota(u)}>Set quota</button>{/if}</td>
            </tr>
          {/each}
        </tbody>
      </table>
      {#if !anyQuotas}<p class="hint">Quotas are stored now and enforced once enabled on a volume.</p>{/if}
    {/if}
  </div>
{/if}
