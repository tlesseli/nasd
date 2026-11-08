<script>
  import { onMount } from 'svelte'
  import { api } from '../api.js'

  let { hostname } = $props()

  let name = $state('')
  let t = $state(null)
  let zones = $state([])
  let servers = $state('')
  let manual = $state('')
  let msg = $state(null)
  let initialZone = ''

  onMount(async () => {
    name = hostname
    const r = await api.get('/api/time')
    t = r.info
    initialZone = t.timezone
    zones = r.timezones
    servers = t.ntpServers.join(', ')
  })

  // nasd restarts after these changes: wait for it, then reload the page.
  async function waitForRestart() {
    msg = { ok: true, text: 'Applied. The NAS service restarts, reloading in a few seconds…' }
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

  async function saveName(event) {
    event.preventDefault()
    try {
      await api.post('/api/hostname', { name })
      waitForRestart()
    } catch (e) {
      msg = { ok: false, text: e.message }
    }
  }

  async function saveTime(event) {
    event.preventDefault()
    const zoneChanged = t.timezone !== initialZone
    try {
      await api.post('/api/time', { timezone: t.timezone, ntp: t.ntp, ntpServers: servers.split(/[\s,]+/).filter(Boolean), manual })
      if (zoneChanged) waitForRestart()
      else msg = { ok: true, text: 'Time settings saved.' }
    } catch (e) {
      msg = { ok: false, text: e.message }
    }
  }
</script>

{#if msg}<p class={msg.ok ? 'notice' : 'error'}>{msg.text}</p>{/if}

<form class="card create" onsubmit={saveName}>
  <h2>Name</h2>
  <label>
    NAS name
    <input bind:value={name} maxlength="15" spellcheck="false" />
    <span class="hint">How computers see it: <span class="mono">\\{name || '…'}</span> on Windows, <span class="mono">{name || '…'}.local</span> on Mac and Linux. Max 15 characters (a Windows limit). Your browser will show a new certificate warning once.</span>
  </label>
  <div class="actions"><button type="submit" disabled={name === hostname}>Rename</button></div>
</form>

{#if t}
  <form class="card create" onsubmit={saveTime}>
    <h2>Date and time</h2>
    <p class="small">NAS time: <strong>{new Date(t.now).toLocaleString()}</strong> {t.synced ? '· synchronized' : t.ntp ? '· not synchronized yet' : ''}</p>
    <label>
      Time zone
      <select bind:value={t.timezone}>{#each zones as z}<option>{z}</option>{/each}</select>
      <span class="hint">Schedules (backups, checks, power) follow this time zone.</span>
    </label>
    <label class="check"><input type="checkbox" bind:checked={t.ntp} /><span><strong>Set the time automatically</strong> <span class="muted small">· from internet time servers (NTP)</span></span></label>
    {#if t.ntp}
      <label>Time servers <span class="hint">(optional; empty = Debian's default pool)</span>
        <input bind:value={servers} placeholder="be.pool.ntp.org, 192.168.1.1" spellcheck="false" /></label>
    {:else}
      <label>Set the time to <input bind:value={manual} placeholder="2026-11-08 20:15:00" /></label>
    {/if}
    <div class="actions"><button type="submit">Save</button></div>
  </form>
{/if}
