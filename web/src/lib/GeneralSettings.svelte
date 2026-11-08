<script>
  import { onMount } from 'svelte'
  import { api } from '../api.js'

  // How the NAS identifies itself, and its clock. Changes are collected and
  // saved together (the bar at the bottom), like in the mockup.
  let { hostname } = $props()

  let initial = $state(null) // values as loaded
  let form = $state(null)
  let zones = $state([])
  let settings = $state(null)
  let timeInfo = $state(null)
  let busy = $state(false)
  let msg = $state(null)

  async function load() {
    const [t, st] = await Promise.all([api.get('/api/time'), api.get('/api/settings')])
    zones = t.timezones
    timeInfo = t.info
    settings = st
    initial = {
      name: hostname, workgroup: st.workgroup || 'WORKGROUP', timezone: t.info.timezone,
      ntp: t.info.ntp, servers: t.info.ntpServers.join(', '), manual: '',
    }
    form = { ...initial }
  }
  onMount(load)

  const labels = { name: 'Device name', workgroup: 'Workgroup', timezone: 'Time zone', ntp: 'Automatic time', servers: 'NTP servers', manual: 'Manual time' }
  const changed = $derived(form ? Object.keys(labels).filter((k) => form[k] !== initial[k]) : [])

  async function waitForRestart() {
    msg = { ok: true, text: 'Saved. The NAS service restarts to apply the new name or time zone; reloading…' }
    await new Promise((r) => setTimeout(r, 4000))
    for (let i = 0; i < 30; i++) {
      try {
        await api.get('/api/setup')
        location.reload()
        return
      } catch {
        await new Promise((r) => setTimeout(r, 1000))
      }
    }
  }

  async function save() {
    busy = true
    msg = null
    try {
      if (form.workgroup !== initial.workgroup) await api.post('/api/settings', { ...settings, workgroup: form.workgroup })
      if (['timezone', 'ntp', 'servers', 'manual'].some((k) => changed.includes(k))) {
        await api.post('/api/time', { timezone: form.timezone, ntp: form.ntp,
          ntpServers: form.servers.split(/[\s,]+/).filter(Boolean), manual: form.ntp ? '' : form.manual })
      }
      if (form.name !== initial.name) await api.post('/api/hostname', { name: form.name })
      if (form.name !== initial.name || form.timezone !== initial.timezone) return waitForRestart()
      msg = { ok: true, text: 'Saved.' }
      await load()
    } catch (e) {
      msg = { ok: false, text: e.message }
    } finally {
      busy = false
    }
  }
</script>

{#if form}
  <section class="card">
    <div class="card-head"><div><h2>General</h2><div class="muted small">How this device identifies itself.</div></div></div>
    <div class="form-row">
      <div class="what">Device name<small>Shown on the network, in Finder and Explorer. Max 15 characters.</small></div>
      <input bind:value={form.name} maxlength="15" spellcheck="false" class="mono" aria-label="Device name" />
    </div>
    <div class="form-row">
      <div class="what">Workgroup<small>SMB workgroup (Windows network neighbourhood).</small></div>
      <input bind:value={form.workgroup} maxlength="15" spellcheck="false" class="mono" aria-label="Workgroup" />
    </div>
    <div class="form-row">
      <div class="what">Time zone<small>Used for schedules and logs. Now: {new Date(timeInfo.now).toLocaleString()}{timeInfo.synced ? ' · synchronized' : ''}</small></div>
      <select bind:value={form.timezone} aria-label="Time zone">{#each zones as z}<option>{z}</option>{/each}</select>
    </div>
    <div class="form-row">
      <div class="what">Automatic time<small>From internet time servers (NTP).</small></div>
      <label class="check"><input type="checkbox" bind:checked={form.ntp} /> <span>Set the time automatically</span></label>
    </div>
    {#if form.ntp}
      <div class="form-row">
        <div class="what">NTP servers<small>Comma-separated; empty = Debian's default pool.</small></div>
        <input bind:value={form.servers} placeholder="pool.ntp.org, time.cloudflare.com" spellcheck="false" class="mono" aria-label="NTP servers" />
      </div>
    {:else}
      <div class="form-row">
        <div class="what">Set the time to<small>Format 2026-11-08 20:15:00.</small></div>
        <input bind:value={form.manual} placeholder="2026-11-08 20:15:00" class="mono" aria-label="Manual time" />
      </div>
    {/if}
  </section>

  {#if msg}<p class={msg.ok ? 'notice' : 'error'}>{msg.text}</p>{/if}
  {#if changed.length}
    <div class="savebar">
      <span class="small">{changed.length} unsaved change{changed.length > 1 ? 's' : ''} · {changed.map((k) => labels[k]).join(', ')}</span>
      <span class="spacer"></span>
      <button class="link" onclick={() => (form = { ...initial })} disabled={busy}>Discard</button>
      <button class="save" onclick={save} disabled={busy}>{busy ? 'Saving…' : 'Save changes'}</button>
    </div>
  {/if}
{/if}

<style>
  .save { background: var(--accent); color: #fff; }
</style>
