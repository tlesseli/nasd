<script>
  import { onMount } from 'svelte'
  import { api } from '../api.js'

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
    n = structuredClone(info.settings)
    if (!n.interfaces.length && info.physical.length) n.interfaces = [info.physical[0]]
    if (!n.managed) n.dhcp = true
    dnsText = n.dns.join(', ')
  }
  onMount(() => {
    load()
    const t = setInterval(() => (now = Date.now()), 1000)
    return () => clearInterval(t)
  })

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

  <div class="card list">
    {#each info.current as c}
      <div class="row"><strong>{c.interface}</strong><span class="mono">{c.address}</span><span class="muted small">MTU {c.mtu}</span></div>
    {/each}
    <div class="row"><strong>Gateway</strong><span class="mono">{info.gateway || '—'}</span></div>
  </div>

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
