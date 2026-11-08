<script>
  import { onMount } from 'svelte'
  import { api } from '../api.js'

  let data = $state(null)
  let p = $state(null)
  let offTime = $state('23:00')
  let onTime = $state('07:00')
  let msg = $state(null)
  const days = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']
  const pad = (n) => String(n).padStart(2, '0')

  async function load() {
    data = await api.get('/api/power')
    p = structuredClone(data.settings)
    if (!p.ups.driver) p.ups.driver = 'usbhid-ups'
    offTime = `${pad(p.timer.off.hour)}:${pad(p.timer.off.minute)}`
    onTime = `${pad(p.timer.onHour)}:${pad(p.timer.onMinute)}`
  }
  onMount(() => {
    load()
    const t = setInterval(async () => (data.ups = (await api.get('/api/power')).ups), 5000) // live UPS status
    return () => clearInterval(t)
  })

  async function save(event) {
    event.preventDefault()
    const [oh, om] = offTime.split(':').map(Number)
    const [nh, nm] = onTime.split(':').map(Number)
    const body = { ...p, spinDownMinutes: Number(p.spinDownMinutes),
      timer: { off: { ...p.timer.off, weekday: Number(p.timer.off.weekday), hour: oh, minute: om }, onHour: nh, onMinute: nm } }
    msg = null
    try {
      await api.post('/api/power', body)
      msg = { ok: true, text: 'Saved and applied.' }
      load()
    } catch (e) {
      msg = { ok: false, text: e.message }
    }
  }

  const wolNICs = $derived(data?.nics.filter((n) => n.wolSupported) ?? [])
</script>

{#if p}
  <form class="card create" onsubmit={save}>
    <fieldset>
      <legend>Disk spin-down</legend>
      <label>
        Spin disks down after
        <select bind:value={p.spinDownMinutes}>
          {#each data.spinDownChoices as m}
            <option value={m}>{m === 0 ? 'Never' : m < 60 ? `${m} minutes of inactivity` : `${m / 60} hour${m > 60 ? 's' : ''} of inactivity`}</option>
          {/each}
        </select>
      </label>
      <p class="hint">Saves power and noise when nobody uses the NAS. The first access after a nap takes ~10 s while the disks spin up. Frequent sleep/wake cycles wear disks, so prefer 30 minutes or more.</p>
    </fieldset>

    <fieldset>
      <legend>Power schedule</legend>
      <div class="add">
        <select bind:value={p.timer.off.kind} aria-label="Power schedule">
          <option value="">Always on</option>
          <option value="daily">Power off every day</option>
          <option value="weekly">Power off every week</option>
        </select>
        {#if p.timer.off.kind === 'weekly'}
          <select bind:value={p.timer.off.weekday} aria-label="Day">{#each days as d, i}<option value={i}>{d}</option>{/each}</select>
        {/if}
        {#if p.timer.off.kind}
          <label class="inline">at <input type="time" bind:value={offTime} /></label>
          <label class="inline">and back on at <input type="time" bind:value={onTime} /></label>
        {/if}
      </div>
      {#if data.nextWake}<p class="hint">Next scheduled wake-up: {new Date(data.nextWake).toLocaleString()}.</p>{/if}
      <p class="hint">The NAS waits for running operations (rebuilds, backups) to finish, up to an hour. Powering back on uses the motherboard clock alarm; check it works once on your hardware.</p>
    </fieldset>

    <fieldset>
      <legend>UPS (uninterruptible power supply)</legend>
      <label class="check">
        <input type="checkbox" bind:checked={p.ups.enabled} />
        <span><strong>A UPS is connected by USB</strong> <span class="muted small">· on a power cut you get an alert; when the battery runs low, the NAS shuts down cleanly</span></span>
      </label>
      {#if p.ups.enabled}
        <label>
          Driver
          <select bind:value={p.ups.driver}>
            <option value="usbhid-ups">usbhid-ups: most USB UPS (APC, Eaton, CyberPower…)</option>
            <option value="nutdrv_qx">nutdrv_qx: many budget UPS</option>
            <option value="blazer_usb">blazer_usb: older budget UPS</option>
            <option value="dummy-ups">Simulation (no real UPS, for testing)</option>
          </select>
        </label>
        {#if data.ups.enabled}
          {#if data.ups.error}
            <p class="error small">{data.ups.error}</p>
          {:else}
            <p class="small">
              <span class="badge {data.ups.lowBattery ? 'bad' : data.ups.onBattery ? 'warn' : 'good'}">
                {data.ups.lowBattery ? 'Battery low' : data.ups.onBattery ? 'On battery' : 'On mains power'}</span>
              {data.ups.model} · battery {data.ups.charge}% · about {Math.round(data.ups.runtimeSec / 60)} min · load {data.ups.load}%
            </p>
          {/if}
        {/if}
      {/if}
    </fieldset>

    <fieldset>
      <legend>Wake-on-LAN</legend>
      {#if wolNICs.length}
        <label class="check">
          <input type="checkbox" bind:checked={p.wakeOnLan} />
          <span>Power on when a "magic packet" arrives on {wolNICs.map((n) => `${n.name} (${n.mac})`).join(', ')}</span>
        </label>
        <p class="hint">From another computer: <span class="mono">wakeonlan {wolNICs[0].mac}</span></p>
      {:else}
        <p class="muted small">No network card here supports Wake-on-LAN (normal in a virtual machine).</p>
      {/if}
    </fieldset>

    {#if msg}<p class={msg.ok ? 'good-text small' : 'error small'}>{msg.text}</p>{/if}
    <div class="actions"><button type="submit">Save</button></div>
  </form>
{/if}
