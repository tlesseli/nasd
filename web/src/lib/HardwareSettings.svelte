<script>
  import { onMount } from 'svelte'
  import { api } from '../api.js'

  let hw = $state(null)
  let backups = $state([])
  let button = $state(null)
  let fan = $state(null)
  let learning = $state(false)
  let msg = $state(null)

  async function load(keepForms = false) {
    hw = await api.get('/api/hardware')
    if (!keepForms) {
      backups = await api.get('/api/backups')
      button = structuredClone(hw.button)
      fan = structuredClone(hw.fan)
      if (!fan.minPercent) Object.assign(fan, { minPercent: 30, lowTemp: 35, highTemp: 45 })
      if (!fan.pwm && hw.outputs.length) fan.pwm = hw.outputs[0].id
    }
  }
  onMount(() => {
    load()
    const t = setInterval(() => load(true), 5000) // live fan status
    return () => clearInterval(t)
  })

  async function learn() {
    learning = true
    msg = null
    try {
      const k = await api.post('/api/button/learn')
      button.device = k.device
      button.code = k.code
      msg = { ok: true, text: `Got it: key ${k.code} from “${k.device}”. Choose the jobs and save.` }
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

  const jobName = (id) => backups.find((b) => b.id === id)?.name ?? `job ${id}`
  const enableText = { 0: 'full speed', 1: 'manual (nasd)', 2: 'automatic (motherboard)' }
</script>

{#if msg}<p class={msg.ok ? 'notice' : 'error'}>{msg.text}</p>{/if}

{#if button}
  <div class="card create">
    <h2>Backup button</h2>
    <p class="muted small">The button on the front of the NAS runs backup jobs, one after the other.</p>
    <div class="add">
      {#if button.device}
        <span class="small">Responds to key <strong>{button.code}</strong> from <strong>{button.device}</strong></span>
      {:else}
        <span class="muted small">Not set up yet: click “Learn”, then press the button on the NAS.</span>
      {/if}
      <span class="spacer"></span>
      <button class="secondary" onclick={learn} disabled={learning}>{learning ? 'Press the button now… (30 s)' : 'Learn'}</button>
    </div>

    <fieldset>
      <legend>Jobs to run</legend>
      {#each backups as b (b.id)}
        <label class="check"><input type="checkbox" checked={button.jobs.includes(b.id)} onchange={(e) => toggleJob(b.id, e.currentTarget.checked)} /><span>{b.name}</span></label>
      {:else}
        <p class="muted small">No backup jobs yet: create them on the Backups page.</p>
      {/each}
      {#if button.jobs.length > 1}
        <p class="small">Order:</p>
        <ol class="order">
          {#each button.jobs as id, i (id)}
            <li>{jobName(id)}
              <button type="button" class="link" onclick={() => move(i, -1)} disabled={i === 0} aria-label="Move up">↑</button>
              <button type="button" class="link" onclick={() => move(i, 1)} disabled={i === button.jobs.length - 1} aria-label="Move down">↓</button>
            </li>
          {/each}
        </ol>
      {/if}
    </fieldset>
    <div class="actions">
      <button class="secondary" onclick={() => post('/api/button/press', {}, 'Button press simulated: jobs started (see Backups).')} disabled={hw.buttonRunning}>
        {hw.buttonRunning ? 'Jobs running…' : 'Simulate a press'}</button>
      <button onclick={() => post('/api/button', button, 'Backup button saved.')}>Save</button>
    </div>
  </div>
{/if}

{#if fan}
  <div class="card create">
    <h2>Fan</h2>
    {#if hw.outputs.length}
      <table class="members">
        <thead><tr><th>Output</th><th>Chip</th><th>Speed</th><th>Mode</th></tr></thead>
        <tbody>
          {#each hw.outputs as o (o.id)}
            <tr><td class="mono">{o.id}</td><td>{o.chip}</td>
              <td>{o.fanRpm >= 0 ? `${o.fanRpm} RPM · ` : ''}{Math.round((o.value * 100) / 255)}%</td>
              <td class="small">{enableText[o.enable] ?? o.enable}</td></tr>
          {/each}
        </tbody>
      </table>
      <label>
        Control
        <select bind:value={fan.mode}>
          <option value="">By the motherboard (default)</option>
          <option value="curve">By the NAS, from disk temperatures</option>
          <option value="full">Always full speed</option>
        </select>
      </label>
      {#if fan.mode}
        <label>Output <select bind:value={fan.pwm}>{#each hw.outputs as o (o.id)}<option>{o.id}</option>{/each}</select></label>
      {/if}
      {#if fan.mode === 'curve'}
        <div class="two-cols">
          <label>Minimum speed (%) <input type="number" min="20" max="100" bind:value={fan.minPercent} /></label>
          <label>Up to (°C) <input type="number" min="20" max="69" bind:value={fan.lowTemp} /></label>
          <label>Full speed from (°C) <input type="number" min="21" max="70" bind:value={fan.highTemp} /></label>
        </div>
        <p class="hint">Between the two temperatures the speed rises gradually. Without any disk temperature, or if a sensor reaches its critical limit, the fan runs at full speed. If the NAS service stops, systemd sets it to full speed.</p>
      {/if}
      {#if hw.fanStatus.mode}
        <p class="small">Now: <strong>{hw.fanStatus.percent}%</strong> · {hw.fanStatus.reason}</p>
      {/if}
      <div class="actions"><button onclick={() => post('/api/fan', { ...fan, minPercent: +fan.minPercent, lowTemp: +fan.lowTemp, highTemp: +fan.highTemp }, 'Fan settings saved.')}>Save</button></div>
    {:else}
      <p class="muted small">No controllable fan found. Normal in a virtual machine; on the ReadyNAS the sensor chip's driver must be loaded (milestone 6).</p>
    {/if}
  </div>
{/if}
