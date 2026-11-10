<script>
  import { onMount } from 'svelte'
  import { api } from '../api.js'
  import { t, N_ } from '../i18n.svelte.js'

  // Who may reach the NAS: off = everyone (the router alone protects it),
  // on = only private networks (or only the NAS's own network) plus the
  // networks listed. Changes must be kept within two minutes, else undone.
  let info = $state(null)
  let f = $state(null) // the form
  let allowText = $state('')
  let msg = $state(null)
  let busy = $state(false)
  let now = $state(Date.now())

  async function load(resetForm = true) {
    try {
      info = await api.get('/api/firewall')
      if (resetForm || !f) {
        f = $state.snapshot(info.settings)
        allowText = f.allow.join('\n')
      }
    } catch (e) {
      msg = { ok: false, text: e.message }
    }
  }
  onMount(() => {
    load()
    const tick = setInterval(() => (now = Date.now()), 1000)
    const poll = setInterval(() => load(false), 10000)
    return () => (clearInterval(tick), clearInterval(poll))
  })

  async function apply(event) {
    event.preventDefault()
    msg = null
    busy = true
    try {
      await api.post('/api/firewall', { ...f, allow: allowText.split(/[\s,]+/).filter(Boolean) })
      msg = f.enabled ? null : { ok: true, text: t('Firewall switched off.') }
      await load()
    } catch (e) {
      msg = { ok: false, text: e.message }
    }
    busy = false
  }
  async function decide(path, text) {
    try {
      await api.post(path)
      msg = { ok: true, text }
      load()
    } catch (e) {
      msg = { ok: false, text: e.message }
    }
  }

  let secondsLeft = $derived(info?.pending ? Math.max(0, Math.round((new Date(info.pending.deadline) - now) / 1000)) : 0)
  const when = (d) => new Date(d).toLocaleString([], { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit', second: '2-digit' })

  // What each listening program is, in words.
  const byProcess = {
    nasd: N_('Web interface (this page)'),
    sshd: N_('SSH (remote command line)'),
    smbd: N_('File sharing (Windows, Mac)'),
    nmbd: N_('Windows network names (NetBIOS)'),
    wsdd2: N_('Shows the NAS in Windows "Network"'),
    'avahi-daemon': N_('Shows the NAS on Macs and Linux (Bonjour)'),
    'systemd-network': N_('Gets the address from the router (DHCP)'),
    vsftpd: N_('FTP'),
    rsync: N_('rsync (backups from other machines)'),
    rpcbind: N_('NFS (Linux file sharing)'),
    'rpc.mountd': N_('NFS (Linux file sharing)'),
    upsd: N_('UPS sharing (NUT)'),
  }
  function serviceName(p) {
    if (p.port === 5355) return t('Name lookup on the network (LLMNR)')
    if (p.process === 'systemd-resolve') return t('Name lookup (for the NAS itself)')
    return byProcess[p.process] ? t(byProcess[p.process]) : p.process || '—'
  }
  // One row per service and port; TCP and UDP together.
  let ports = $derived.by(() => {
    const rows = []
    for (const p of info?.ports ?? []) {
      const row = rows.find((r) => r.port === p.port && r.process === p.process)
      if (row) {
        if (!row.protos.includes(p.proto)) row.protos.push(p.proto)
        row.local &&= p.local
      } else rows.push({ ...p, protos: [p.proto] })
    }
    return rows
  })
  let on = $derived(info?.settings.enabled && info?.active)
</script>

{#if msg}<p class={msg.ok ? 'notice' : 'error'}>{msg.text}</p>{/if}

{#if info && f}
  {#if info.pending}
    <div class="card pending">
      <h2>{t('Keep the new firewall settings?')}</h2>
      <p>{@html t('Check that your other devices still reach the NAS (open a share, connect with SSH). Without an answer, the previous settings come back in <strong>{seconds} s</strong>.', { seconds: secondsLeft })}</p>
      <div class="actions">
        <button class="secondary" onclick={() => decide('/api/firewall/undo', t('Previous firewall settings restored.'))}>{t('Undo')}</button>
        <button onclick={() => decide('/api/firewall/keep', t('Firewall settings kept.'))}>{t('Keep these settings')}</button>
      </div>
    </div>
  {/if}

  <section class="card create">
    <h2>
      {t('Firewall')}
      <span class="badge {on ? 'good' : info.settings.enabled ? 'bad' : 'neutral'}">{on ? t('On') : info.settings.enabled ? t('On, but not loaded') : t('Off')}</span>
    </h2>
    <p class="hint">
      {on
        ? info.settings.scope === 'local'
          ? t('Only devices on the NAS’s own network, and the networks listed below, can reach it. Everything else is dropped.')
          : t('Only devices on private networks (home, office, VPN), and the networks listed below, can reach the NAS. Connections from the internet are dropped.')
        : t('Every device that can reach the NAS’s address may connect: the NAS relies on your router alone. That is fine as long as no port is forwarded to it and the router blocks incoming IPv6.')}
    </p>
    {#if on}
      <p class="small">
        {info.blocked === 1 ? t('{n} attempt blocked since the NAS started.', { n: info.blocked }) : t('{n} attempts blocked since the NAS started.', { n: info.blocked.toLocaleString() })}
      </p>
    {/if}

    <form onsubmit={apply}>
      <label class="check">
        <input type="checkbox" bind:checked={f.enabled} disabled={!!info.pending} />
        <span><strong>{t('Firewall on')}</strong></span>
      </label>
      {#if f.enabled}
        <fieldset class="scope" disabled={!!info.pending}>
          <label class="check">
            <input type="radio" bind:group={f.scope} value="private" />
            <span>{t('Private networks')} <span class="muted small">{t('· any home, office or VPN address (recommended: works through a mesh or a second router)')}</span></span>
          </label>
          <label class="check">
            <input type="radio" bind:group={f.scope} value="local" />
            <span>{t('The NAS’s network only')} <span class="muted small mono">· {info.local.join(', ') || '—'}</span></span>
          </label>
        </fieldset>
        <label class="field">
          <span>{t('Also allow')} <span class="muted small">{t('· addresses or networks, one per line (e.g. 192.168.68.0/22, or 100.64.0.0/10 for Tailscale)')}</span></span>
          <textarea rows="3" class="mono" bind:value={allowText} disabled={!!info.pending} placeholder="192.168.68.0/22"></textarea>
        </label>
      {/if}
      <p class="small">
        {t('Your computer:')} <span class="mono">{info.client}</span>
        {#if info.clientAllowed}<span class="badge good">{t('allowed')}</span>
        {:else}<span class="badge warn">{t('not in the allowed networks')}</span>{/if}
      </p>
      {#if f.enabled && !info.installed}<p class="hint">{t('The firewall tool (nftables) is installed when you switch it on: the NAS needs internet access for a minute.')}</p>{/if}
      <p class="hint">{t('A change must be kept within two minutes, otherwise it is undone. A change that would block your own computer is refused. If you ever lock yourself out, hold the Reset button at the back: it also switches the firewall off.')}</p>
      <div class="actions"><button disabled={busy || !!info.pending}>{busy ? t('Applying…') : t('Apply')}</button></div>
    </form>
  </section>

  <section class="card create">
    <h2>{t('Open on the NAS')}</h2>
    <p class="hint">{t('The programs waiting for connections right now. What is switched off in Services doesn’t show here.')}</p>
    <table class="members">
      <thead><tr><th>{t('Service')}</th><th>{t('Port')}</th><th>{t('Reachable from')}</th></tr></thead>
      <tbody>
        {#each ports as p (p.process + p.port)}
          <tr>
            <td>{serviceName(p)} <span class="muted small mono">{p.process}</span></td>
            <td class="mono small">{p.port} {p.protos.join('+')}</td>
            <td class="small">
              {#if p.local}<span class="badge neutral">{t('the NAS itself')}</span>
              {:else if on}<span class="badge good">{t('allowed networks')}</span>
              {:else}<span class="badge warn">{t('everyone')}</span>{/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </section>

  {#if on || info.recent.length}
    <section class="card create">
      <h2>{t('Recently blocked')}</h2>
      {#if info.recent.length}
        <table class="members">
          <thead><tr><th>{t('Time')}</th><th>{t('From')}</th><th>{t('To port')}</th></tr></thead>
          <tbody>
            {#each info.recent as a, i (i)}
              <tr><td class="small">{when(a.time)}</td><td class="mono small">{a.source}</td><td class="mono small">{a.port || '—'} {a.proto}</td></tr>
            {/each}
          </tbody>
        </table>
        <p class="hint">{t('At most 10 a minute are noted, since the NAS started.')}</p>
      {:else}
        <p class="muted small">{t('Nothing blocked yet.')}</p>
      {/if}
    </section>
  {/if}
{/if}

<style>
  h2 { display: flex; align-items: center; gap: 0.5rem; }
  form { display: flex; flex-direction: column; gap: 0.8rem; margin-top: 0.8rem; }
  .scope { border: 0; padding: 0 0 0 1.6rem; margin: 0; display: flex; flex-direction: column; gap: 0.4rem; }
  .field { display: flex; flex-direction: column; gap: 0.3rem; }
  textarea { width: 100%; max-width: 32rem; }
</style>
