<script>
  import { onMount } from 'svelte'
  import { api } from '../api.js'
  import Gauge from '../ui/Gauge.svelte'
  import { t, N_ } from '../i18n.svelte.js'

  let hw = $state(null)
  let backups = $state([])
  let button = $state(null)
  let fan = $state(null)
  let learning = $state(false)
  let msg = $state(null)
  let lcd = $state(null)

  async function load(keepForms = false) {
    hw = await api.get('/api/hardware')
    if (!keepForms) {
      backups = await api.get('/api/backups')
      button = $state.snapshot(hw.button)
      fan = $state.snapshot(hw.fan)
      if (!fan.mode) fan.mode = 'full' // never the chip's automatic mode
      if (!fan.minPercent) Object.assign(fan, { minPercent: 40, lowTemp: 38, highTemp: 50 })
      if (!fan.fixedPercent) fan.fixedPercent = 50
      if (!hw.outputs.find((o) => o.id === fan.pwm)?.hasFan) fan.pwm = hw.outputs.find((o) => o.hasFan)?.id ?? ''
    }
  }
  async function loadLCD() {
    lcd = await api.get('/api/lcd').catch(() => lcd)
  }
  onMount(() => {
    load()
    loadLCD()
    const t = setInterval(() => load(true), 5000) // live fan status
    const d = setInterval(loadLCD, 1000) // live display preview
    return () => (clearInterval(t), clearInterval(d))
  })

  async function learn() {
    learning = true
    msg = null
    try {
      const k = await api.post('/api/button/learn')
      button.device = k.device
      button.code = k.code
      msg = { ok: true, text: t('Got it: key {code} from “{device}”. Choose the jobs and save.', { code: k.code, device: k.device }) }
    } catch (e) {
      msg = { ok: false, text: e.message }
    } finally {
      learning = false
    }
  }

  function toggleJob(id, on) {
    button.jobs = on ? [...button.jobs, id] : button.jobs.filter((j) => j !== id)
  }
  function move(i, d) {
    const j = [...button.jobs]
    ;[j[i], j[i + d]] = [j[i + d], j[i]]
    button.jobs = j
  }

  async function post(path, body, ok) {
    msg = null
    try {
      await api.post(path, body)
      msg = { ok: true, text: ok }
      load(true)
    } catch (e) {
      msg = { ok: false, text: e.message }
    }
  }

  const jobName = (id) => backups.find((b) => b.id === id)?.name ?? t('job {id}', { id })
  const tierText = { phase: N_('startup / shutdown'), critical: N_('critical problem (blinking)'), network: N_('network change to confirm'),
    ups: N_('power cut'), feedback: N_('button / locate'), operation: N_('operation in progress'), warning: N_('warning'), routine: N_('routine information') }
  const enableText = { 0: N_('full speed'), 1: N_('manual (nasd)'), 2: N_('automatic (motherboard)') }
  // Fan test of one output (a job, about 90 s): the result shows in the table.
  async function testFan(id) {
    msg = null
    try {
      await api.post('/api/fan/test', { pwm: id })
      msg = { ok: true, text: t('Fan test started on {output}: about a minute and a half. The fan speeds up and slows down meanwhile.', { output: id }) }
      const wait = setInterval(async () => {
        await load(true)
        if (!hw.fanStatus.testing) {
          clearInterval(wait)
          await load() // new minimum and fan detection
        }
      }, 3000)
    } catch (e) {
      msg = { ok: false, text: e.message }
    }
  }
  let chosen = $derived(hw?.outputs.find((o) => o.id === fan?.pwm))
  let minAllowed = $derived(chosen?.minPercent ?? 20)
  const outState = (o) => !o.input ? t('no speed sensor') : !o.tested ? (o.hasFan ? t('fan · not tested yet') : t('not tested yet'))
    : o.hasFan ? t('fan · minimum {percent}%', { percent: o.minPercent }) : t('no fan')

  // The controlled output (or the first one with a fan), for the gauges.
  let gaugeOut = $derived(hw?.outputs.find((o) => o.id === (hw.fan.pwm || fan?.pwm)) ?? hw?.outputs.find((o) => o.fanRpm >= 0))
  let rpmMax = $derived(Math.max(hw?.fan.maxRpm || 0, gaugeOut?.fanRpm || 0, 1000))
  const SAFETY = 50 // °C: fixed speed goes to 100% from there (FixedSafetyTemp)
</script>

{#if msg}<p class={msg.ok ? 'notice' : 'error'}>{msg.text}</p>{/if}

{#if lcd}
  <div class="card create">
    <div class="card-head"><h2>{t('Front display')}</h2><span class="spacer"></span><span class="muted small">{lcd.driver}</span></div>
    <div class="lcd-wrap">
      <div class="lcd" class:off={!lcd.frame.backlight} role="img" aria-label={t('Front display: {line1} / {line2}', { line1: lcd.frame.line1.trim(), line2: lcd.frame.line2.trim() })}>
        <span>{lcd.frame.line1}</span><span>{lcd.frame.line2}</span>
      </div>
      <div class="small muted">{t('Showing:')} <strong>{tierText[lcd.frame.tier] ? t(tierText[lcd.frame.tier]) : lcd.frame.tier}</strong><br />
        {t('Problems and operations take over the display; otherwise it rotates through name and address, capacity, temperatures and uptime every 5 s.')}</div>
    </div>
    <div class="add">
      <label class="check"><input type="checkbox" checked={lcd.settings.alwaysOn}
        onchange={(e) => post('/api/lcd', { alwaysOn: e.currentTarget.checked }, t('Display setting saved.')).then(loadLCD)} />
        <span>{t('Backlight always on')} <span class="muted small">{t('· otherwise it lights for 2 minutes after the Backup button is pressed, and whenever something needs attention')}</span></span></label>
      <span class="spacer"></span>
      <button class="secondary" onclick={() => post('/api/lcd/wake', {}, t('Display lit for 2 minutes.')).then(loadLCD)}>{t('Light up')}</button>
    </div>
  </div>
{/if}

{#if button}
  <div class="card create">
    <h2>{t('Backup button')}</h2>
    <p class="muted small">{t('The button on the front of the NAS runs backup jobs, one after the other.')}</p>
    <div class="add">
      {#if hw.buttonBuiltIn}
        <span class="small">{@html t('The <strong>Backup button</strong> on the front of the NAS is ready: a short press runs the jobs below.')}
          <span class="muted">{t('Holding it 10 seconds instead resets the admin password: the new one appears on the front display (for when it is lost).')}</span></span>
      {:else if button.device}
        <span class="small">{t('Responds to key')} <strong>{button.code}</strong> {t('from')} <strong>{button.device}</strong></span>
      {:else}
        <span class="muted small">{t('Not set up yet: click “Learn”, then press the button on the NAS.')}</span>
      {/if}
      {#if !hw.buttonBuiltIn}
        <span class="spacer"></span>
        <button class="secondary" onclick={learn} disabled={learning}>{learning ? t('Press the button now… (30 s)') : t('Learn')}</button>
      {/if}
    </div>

    <fieldset>
      <legend>{t('Jobs to run')}</legend>
      {#each backups as b (b.id)}
        <label class="check"><input type="checkbox" checked={button.jobs.includes(b.id)} onchange={(e) => toggleJob(b.id, e.currentTarget.checked)} /><span>{b.name}</span></label>
      {:else}
        <p class="muted small">{t('No backup jobs yet: create them on the Backups page.')}</p>
      {/each}
      {#if button.jobs.length > 1}
        <p class="small">{t('Order:')}</p>
        <ol class="order">
          {#each button.jobs as id, i (id)}
            <li>{jobName(id)}
              <button type="button" class="link" onclick={() => move(i, -1)} disabled={i === 0} aria-label={t('Move up')}>↑</button>
              <button type="button" class="link" onclick={() => move(i, 1)} disabled={i === button.jobs.length - 1} aria-label={t('Move down')}>↓</button>
            </li>
          {/each}
        </ol>
      {/if}
    </fieldset>
    <div class="actions">
      <button class="secondary" onclick={() => post('/api/button/press', {}, t('Button press simulated: jobs started (see Backups).'))} disabled={hw.buttonRunning}>
        {hw.buttonRunning ? t('Jobs running…') : t('Simulate a press')}</button>
      <button onclick={() => post('/api/button', button, t('Backup button saved.'))}>{t('Save')}</button>
    </div>
  </div>
{/if}

{#if fan}
  <div class="card create">
    <h2>{t('Fan')}</h2>
    {#if hw.outputs.length}
      <div class="gauges">
        {#if gaugeOut && gaugeOut.fanRpm >= 0}
          <Gauge label={t('Fan speed')} value={gaugeOut.fanRpm} max={rpmMax} text="{gaugeOut.fanRpm} RPM"
            warn={gaugeOut.minRpm > 0 && gaugeOut.fanRpm < gaugeOut.minRpm}
            marks={gaugeOut.minRpm > 0 ? [{ value: gaugeOut.minRpm, label: t('alarm below {rpm} RPM', { rpm: gaugeOut.minRpm }) }] : []} />
        {/if}
        {#if hw.fanStatus.hottestDisk > 0 || hw.fan.mode}
          <Gauge label={t('Hottest disk')} value={hw.fanStatus.hottestDisk} min={20} max={70}
            text={hw.fanStatus.hottestDisk ? `${hw.fanStatus.hottestDisk} °C` : '—'} warn={hw.fanStatus.hottestDisk >= 50}
            marks={hw.fan.mode === 'curve' ? [{ value: hw.fan.lowTemp, label: t('{temp} °C: minimum speed', { temp: hw.fan.lowTemp }) }, { value: hw.fan.highTemp, label: t('{temp} °C: full speed', { temp: hw.fan.highTemp }) }]
              : hw.fan.mode === 'fixed' ? [{ value: SAFETY, label: t('{temp} °C: full speed', { temp: SAFETY }) }] : []} />
        {/if}
        {#if hw.fanStatus.mode}
          <Gauge label={t('Speed set by the NAS')} value={hw.fanStatus.percent} text="{hw.fanStatus.percent}%"
            marks={hw.fan.mode === 'curve' ? [{ value: hw.fan.minPercent, label: t('minimum {percent}%', { percent: hw.fan.minPercent }) }]
              : hw.fan.mode === 'fixed' ? [{ value: hw.fan.fixedPercent, label: t('chosen {percent}%', { percent: hw.fan.fixedPercent }) }] : []} />
        {/if}
      </div>
      <table class="members">
        <thead><tr><th>{t('Output')}</th><th>{t('Fan')}</th><th>{t('Speed')}</th><th>{t('Mode')}</th><th></th></tr></thead>
        <tbody>
          {#each hw.outputs as o (o.id)}
            <tr class:inactive={o.tested ? !o.hasFan : !o.input}><td class="mono">{o.id} <span class="muted small">{o.chip}</span></td>
              <td class="small">{outState(o)}</td>
              <td>{o.fanRpm >= 0 ? `${o.fanRpm} RPM · ` : ''}{Math.round((o.value * 100) / 255)}%</td>
              <td class="small">{enableText[o.enable] ? t(enableText[o.enable]) : o.enable}</td>
              <td class="right">{#if o.input}<button class="link" onclick={() => testFan(o.id)} disabled={!!hw.fanStatus.testing}>
                {hw.fanStatus.testing === o.id ? t('Testing…') : o.tested ? t('Test again') : t('Test')}</button>{/if}</td></tr>
          {/each}
        </tbody>
      </table>
      <label>
        {t('Control')}
        <select bind:value={fan.mode}>
          <option value="curve">{t('By the NAS, from disk temperatures')}</option>
          <option value="fixed">{t('Fixed speed')}</option>
          <option value="full">{t('Always full speed')}</option>
        </select>
      </label>
      {#if fan.mode !== 'full'}
        <label>{t('Output')} <select bind:value={fan.pwm}>
          {#each hw.outputs as o (o.id)}<option value={o.id} disabled={!o.hasFan}>{o.id} · {outState(o)}</option>{/each}
        </select></label>
      {/if}
      {#if fan.mode !== 'full' && chosen}
        <p class="hint">{t('Measured minimum for this fan: {min}% (it turns and restarts from standstill). The NAS never sets it lower.', { min: chosen.minPercent })}</p>
      {/if}
      {#if fan.mode === 'fixed'}
        <label>{t('Speed:')} <strong>{fan.fixedPercent}%</strong>
          <input type="range" min={minAllowed} max="100" step="5" bind:value={fan.fixedPercent} /></label>
        <p class="hint">{t('The fan keeps this speed. If a disk reaches {temp} °C, it goes to full speed until the disk cools down.', { temp: SAFETY })}</p>
      {/if}
      {#if fan.mode === 'curve'}
        <div class="two-cols">
          <label>{t('Minimum speed (%)')} <input type="number" min={minAllowed} max="100" bind:value={fan.minPercent} /></label>
          <label>{t('Up to (°C)')} <input type="number" min="20" max="69" bind:value={fan.lowTemp} /></label>
          <label>{t('Full speed from (°C)')} <input type="number" min="21" max="70" bind:value={fan.highTemp} /></label>
        </div>
        <p class="hint">{t('Between the two temperatures the speed rises gradually. Without any disk temperature, or if a sensor reaches its critical limit, the fan runs at full speed. If the NAS service stops, systemd sets it to full speed.')}</p>
      {/if}
      {#if hw.fanStatus.mode}
        <p class="small">{t('Now:')} <strong>{hw.fanStatus.percent}%</strong> · {hw.fanStatus.reason}</p>
      {/if}
      <div class="actions"><button onclick={() => post('/api/fan', { ...fan, minPercent: +fan.minPercent, lowTemp: +fan.lowTemp, highTemp: +fan.highTemp, fixedPercent: +fan.fixedPercent }, t('Fan settings saved.'))}>{t('Save')}</button></div>
    {:else}
      <p class="muted small">{t("No controllable fan found. Normal in a virtual machine; on the ReadyNAS the sensor chip's driver must be loaded.")}</p>
    {/if}
  </div>
{/if}

<style>
  .lcd-wrap { display: flex; gap: 1.25rem; align-items: center; flex-wrap: wrap; }
  /* A 2x16 blue character LCD, as on the ReadyNAS front. */
  .lcd { display: flex; flex-direction: column; padding: 0.7rem 0.9rem; border-radius: 6px; border: 3px solid #0b0d12;
    background: #1d3fd8; color: #e8f0ff; font-family: ui-monospace, 'DejaVu Sans Mono', monospace; font-size: 1.15rem;
    letter-spacing: 0.08em; line-height: 1.35; white-space: pre; box-shadow: inset 0 0 18px rgb(0 0 0 / 0.35); }
  .lcd.off { background: #101a44; color: #5866a3; }
  tr.inactive td { opacity: 0.45; }
  .gauges { display: flex; flex-wrap: wrap; gap: 1.5rem; justify-content: space-around; margin: 0.5rem 0 1rem; }
</style>
