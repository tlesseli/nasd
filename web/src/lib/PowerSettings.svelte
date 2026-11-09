<script>
  import { onMount } from 'svelte'
  import { api } from '../api.js'
  import { t, N_ } from '../i18n.svelte.js'

  let data = $state(null)
  let p = $state(null)
  let offTime = $state('23:00')
  let onTime = $state('07:00')
  let msg = $state(null)
  const days = [N_('Sunday'), N_('Monday'), N_('Tuesday'), N_('Wednesday'), N_('Thursday'), N_('Friday'), N_('Saturday')]
  const pad = (n) => String(n).padStart(2, '0')

  async function load() {
    data = await api.get('/api/power')
    p = $state.snapshot(data.settings)
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
      msg = { ok: true, text: t('Saved and applied.') }
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
      <legend>{t('Disk spin-down')}</legend>
      <label>
        {t('Spin disks down after')}
        <select bind:value={p.spinDownMinutes}>
          {#each data.spinDownChoices as m}
            <option value={m}>{m === 0 ? t('Never') : m < 60 ? t('{n} minutes of inactivity', { n: m }) : m > 60 ? t('{n} hours of inactivity', { n: m / 60 }) : t('{n} hour of inactivity', { n: m / 60 })}</option>
          {/each}
        </select>
      </label>
      <p class="hint">{t('Saves power and noise when nobody uses the NAS. The first access after a nap takes ~10 s while the disks spin up. Frequent sleep/wake cycles wear disks, so prefer 30 minutes or more.')}</p>
    </fieldset>

    <fieldset>
      <legend>{t('Power schedule')}</legend>
      <div class="add">
        <select bind:value={p.timer.off.kind} aria-label={t('Power schedule')}>
          <option value="">{t('Always on')}</option>
          <option value="daily">{t('Power off every day')}</option>
          <option value="weekly">{t('Power off every week')}</option>
        </select>
        {#if p.timer.off.kind === 'weekly'}
          <select bind:value={p.timer.off.weekday} aria-label={t('Day')}>{#each days as d, i}<option value={i}>{t(d)}</option>{/each}</select>
        {/if}
        {#if p.timer.off.kind}
          <label class="inline">{t('at')} <input type="time" bind:value={offTime} /></label>
          <label class="inline">{t('and back on at')} <input type="time" bind:value={onTime} /></label>
        {/if}
      </div>
      {#if data.nextWake}<p class="hint">{t('Next scheduled wake-up: {date}.', { date: new Date(data.nextWake).toLocaleString() })}</p>{/if}
      <p class="hint">{t('The NAS waits for running operations (rebuilds, backups) to finish, up to an hour. Powering back on uses the motherboard clock alarm; check it works once on your hardware.')}</p>
    </fieldset>

    <fieldset>
      <legend>{t('UPS (uninterruptible power supply)')}</legend>
      <label class="check">
        <input type="checkbox" bind:checked={p.ups.enabled} />
        <span><strong>{t('A UPS is connected by USB')}</strong> <span class="muted small">{t('· on a power cut you get an alert; when the battery runs low, the NAS shuts down cleanly')}</span></span>
      </label>
      {#if p.ups.enabled}
        <label>
          {t('Driver')}
          <select bind:value={p.ups.driver}>
            <option value="usbhid-ups">{t('usbhid-ups: most USB UPS (APC, Eaton, CyberPower…)')}</option>
            <option value="nutdrv_qx">{t('nutdrv_qx: many budget UPS')}</option>
            <option value="blazer_usb">{t('blazer_usb: older budget UPS')}</option>
            <option value="dummy-ups">{t('Simulation (no real UPS, for testing)')}</option>
          </select>
        </label>
        {#if data.ups.enabled}
          {#if data.ups.error}
            <p class="error small">{data.ups.error}</p>
          {:else}
            <p class="small">
              <span class="badge {data.ups.lowBattery ? 'bad' : data.ups.onBattery ? 'warn' : 'good'}">
                {data.ups.lowBattery ? t('Battery low') : data.ups.onBattery ? t('On battery') : t('On mains power')}</span>
              {t('{model} · battery {charge}% · about {min} min · load {load}%', { model: data.ups.model, charge: data.ups.charge, min: Math.round(data.ups.runtimeSec / 60), load: data.ups.load })}
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
          <span>{t('Power on when a "magic packet" arrives on {nics}', { nics: wolNICs.map((n) => `${n.name} (${n.mac})`).join(', ') })}</span>
        </label>
        <p class="hint">{t('From another computer:')} <span class="mono">wakeonlan {wolNICs[0].mac}</span></p>
      {:else}
        <p class="muted small">{t('No network card here supports Wake-on-LAN (normal in a virtual machine).')}</p>
      {/if}
    </fieldset>

    {#if msg}<p class={msg.ok ? 'good-text small' : 'error small'}>{msg.text}</p>{/if}
    <div class="actions"><button type="submit">{t('Save')}</button></div>
  </form>
{/if}
