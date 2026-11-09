<script>
  import { onMount } from 'svelte'
  import { api, formatBytes } from '../api.js'

  let info = $state(null)
  let bondModes = $state([])
  let n = $state(null)
  let dnsText = $state('')
  let msg = $state(null)
  let now = $state(Date.now())

  async function load() {
    const r = await api.get('/api/network')
    info = r.info
    bondModes = r.bondModes
    n = $state.snapshot(info.settings)
    if (!n.interfaces.length && info.physical.length) n.interfaces = [info.physical[0]]
    if (!n.managed) n.dhcp = true
    dnsText = n.dns.join(', ')
  }
  // Live port status, without touching the form being edited.
  async function refreshPorts() {
    const r = await api.get('/api/network').catch(() => null)
    if (r && info) {
      info.ports = r.info.ports
      info.current = r.info.current
      info.gateway = r.info.gateway
    }
  }
  onMount(() => {
    load()
    const t = setInterval(() => (now = Date.now()), 1000)
    const p = setInterval(refreshPorts, 5000)
    return () => (clearInterval(t), clearInterval(p))
  })
  const speedText = (mbps) => (mbps >= 1000 ? `${mbps / 1000} Gb/s` : `${mbps} Mb/s`)

  async function apply(event) {
    event.preventDefault()
    msg = null
    const body = { ...n, mtu: Number(n.mtu) || 0, dns: dnsText.split(/[\s,]+/).filter(Boolean),
      bond: n.interfaces.length > 1 ? n.bond || 'active-backup' : '' }
    try {
      await api.post('/api/network', body)
      await load()
    } catch (e) {
      msg = { ok: false, text: e.message }
    }
  }

  async function confirm() {
    try {
      await api.post('/api/network/confirm')
      msg = { ok: true, text: 'New network settings kept.' }
      load()
    } catch (e) {
      msg = { ok: false, text: e.message }
    }
  }

  let secondsLeft = $derived(info?.pending ? Math.max(0, Math.round((new Date(info.pending.deadline) - now) / 1000)) : 0)
  let newAddress = $derived(info?.settings.managed && !info.settings.dhcp ? info.settings.address.split('/')[0] : null)
  const modeHelp = {
    'active-backup': 'Failover: one port works, the other takes over if its cable or switch port fails. Works with any switch.',
    'balance-alb': 'Failover + spreads traffic over both ports. Works with any switch.',
    '802.3ad': 'LACP: both ports form one faster link. Requires a managed switch configured for LACP.',
  }
</script>

{#if info}
  {#if info.pending}
    <div class="card pending">
      <h2>Confirm the new network settings</h2>
      <p>If this page still works, the change is fine: keep it. Otherwise the previous settings come back automatically in <strong>{secondsLeft} s</strong>.</p>
      {#if newAddress && !location.hostname.startsWith(newAddress)}
        <p class="small">If the address changed, open <a class="mono" href="https://{newAddress}:{location.port || 443}/#/system">https://{newAddress}:{location.port || 443}</a> and confirm from there.</p>
      {/if}
      <div class="actions"><button onclick={confirm}>Keep these settings</button></div>
    </div>
  {/if}

  <div class="ports">
    {#each info.ports ?? [] as p (p.name)}
      <div class="card port" class:up={p.link}>
        <div class="port-head">
          <!-- An RJ45 socket: lit green when a cable is connected and the link is up. -->
          <svg class="jack" viewBox="0 0 40 32" aria-hidden="true">
            <rect x="1" y="1" width="38" height="30" rx="3" class="jack-body" />
            <path d="M8 8h24v12h-5v5H13v-5H8z" class="jack-hole" />
            {#each [0, 1, 2, 3, 4, 5, 6, 7] as i}<rect x={10.5 + i * 2.6} y="9" width="1.4" height="4" class="jack-pin" />{/each}
            <rect x="4" y="25" width="7" height="3" rx="1" class="led-link" />
            <rect x="29" y="25" width="7" height="3" rx="1" class="led-act" />
          </svg>
          <div>
            <div class="port-title"><strong>{p.label}</strong> <span class="mono muted small">{p.name}</span></div>
            <div class="small">
              {#if p.link}<span class="badge good">Connected</span> {p.speedMbps ? speedText(p.speedMbps) : ''}{p.duplex ? ` · ${p.duplex} duplex` : ''}
              {:else}<span class="badge neutral">No cable</span>{/if}
            </div>
          </div>
        </div>
        <dl class="port-info small">
          <dt>Address</dt><dd class="mono">{p.addresses.length ? p.addresses.join(', ') : '—'}{p.master ? ` (via ${p.master})` : ''}</dd>
          <dt>MAC</dt><dd class="mono">{p.mac}</dd>
          <dt>Traffic</dt><dd>↓ {formatBytes(p.rxBytes)} · ↑ {formatBytes(p.txBytes)}</dd>
          <dt>MTU</dt><dd>{p.mtu}{p.driver ? ` · driver ${p.driver}` : ''}</dd>
        </dl>
      </div>
    {/each}
  </div>
  <p class="small muted gw">Gateway (router): <span class="mono">{info.gateway || '—'}</span></p>

  <form class="card create" onsubmit={apply}>
    <label class="check">
      <input type="checkbox" bind:checked={n.managed} />
      <span><strong>Configure the network here</strong> <span class="muted small">· otherwise the system default applies (automatic address from your router)</span></span>
    </label>

    {#if n.managed}
      <fieldset>
        <legend>Interfaces</legend>
        {#each info.physical as p}
          <label class="check"><input type="checkbox" bind:group={n.interfaces} value={p} /><span class="mono">{p}</span></label>
        {/each}
        {#if n.interfaces.length > 1}
          <label>
            Bonding mode (both ports act as one)
            <select bind:value={n.bond}>
              {#each bondModes as m}<option value={m}>{m}</option>{/each}
            </select>
          </label>
          <p class="hint">{modeHelp[n.bond || 'active-backup']}</p>
        {/if}
      </fieldset>

      <fieldset>
        <legend>Address</legend>
        <label class="check"><input type="radio" bind:group={n.dhcp} value={true} /><span>Automatic (DHCP)</span></label>
        <label class="check"><input type="radio" bind:group={n.dhcp} value={false} /><span>Fixed address <span class="muted small">· recommended for a NAS: its address never changes</span></span></label>
        {#if !n.dhcp}
          <div class="two-cols">
            <label>Address / prefix <input bind:value={n.address} placeholder="192.168.1.50/24" spellcheck="false" /></label>
            <label>Gateway (router) <input bind:value={n.gateway} placeholder="192.168.1.1" spellcheck="false" /></label>
            <label>DNS servers <input bind:value={dnsText} placeholder="192.168.1.1, 1.1.1.1" spellcheck="false" /></label>
          </div>
          <p class="hint">Pick an address outside your router's DHCP range so no other device gets it.</p>
        {/if}
      </fieldset>

      <label>
        MTU
        <select bind:value={n.mtu}>
          <option value={0}>Standard (1500)</option>
          <option value={9000}>Jumbo frames (9000)</option>
        </select>
        <span class="hint">Jumbo frames speed up large transfers only if every switch and computer on the path supports them.</span>
      </label>
    {/if}

    {#if msg}<p class={msg.ok ? 'good-text small' : 'error small'}>{msg.text}</p>{/if}
    <div class="actions"><button type="submit" disabled={!!info.pending}>Apply</button></div>
    <p class="hint">After applying you have 90 seconds to confirm. Without confirmation, the previous settings return: a mistake can't lock you out.</p>
  </form>
{/if}

<style>
  .ports { display: grid; grid-template-columns: repeat(auto-fit, minmax(260px, 1fr)); gap: 14px; }
  .port-head { display: flex; gap: 0.8rem; align-items: center; }
  .port-title { margin-bottom: 0.2rem; }
  .jack { width: 44px; height: 36px; flex: none; }
  .jack-body { fill: var(--surface-2, #2a2d36); stroke: var(--border); }
  .jack-hole { fill: #0b0d12; }
  .jack-pin { fill: #b08d3c; }
  .led-link, .led-act { fill: #3a3d46; }
  .port.up .led-link { fill: #22a06b; }
  .port.up .led-act { fill: #e0a43a; }
  .port-info { display: grid; grid-template-columns: auto 1fr; gap: 0.2rem 0.8rem; margin: 0.8rem 0 0; }
  .port-info dt { color: var(--muted); }
  .port-info dd { margin: 0; overflow-wrap: anywhere; }
  .gw { margin: 0.6rem 0 1rem; }
</style>
