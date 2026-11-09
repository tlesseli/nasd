<script>
  import { onMount } from 'svelte'
  import { api } from '../api.js'
  import Icon from './Icon.svelte'
  import { t, N_ } from '../i18n.svelte.js'

  // Command palette (Ctrl/⌘ K): jump to a page, a settings section, a share,
  // a user or a volume. Arrow keys + Enter, Escape closes.
  let { onClose } = $props()

  let query = $state('')
  let items = $state([])
  let active = $state(0)
  let input = $state()

  const fixed = [
    { label: N_('Overview'), hint: N_('Page'), href: '#/', icon: 'home' },
    { label: N_('Volumes & Disks'), hint: N_('Page'), href: '#/volumes', icon: 'database' },
    { label: N_('Shares'), hint: N_('Page'), href: '#/shares', icon: 'folder' },
    { label: N_('Users'), hint: N_('Page'), href: '#/users', icon: 'users' },
    { label: N_('Backups'), hint: N_('Page'), href: '#/backups', icon: 'archive' },
    ...[['general', N_('General: name, workgroup, time')], ['network', N_('Network')], ['services', N_('Services: SMB, FTP, rsync')],
      ['alerts', N_('Alerts')], ['power', N_('Power & UPS')], ['hardware', N_('Hardware: button, fan')], ['updates', N_('Software update')],
      ['events', N_('Events & logs')], ['restart', N_('Restart / shut down')]].map(([id, label]) => ({
      label, hint: N_('System Settings'), href: `#/system/${id}`, icon: 'settings',
    })),
  ]

  onMount(async () => {
    input?.focus()
    items = fixed.map((i) => ({ ...i, label: t(i.label), hint: t(i.hint) }))
    try {
      const [shares, users, volumes] = await Promise.all([api.get('/api/shares'), api.get('/api/users'), api.get('/api/volumes')])
      items = [
        ...fixed.map((i) => ({ ...i, label: t(i.label), hint: t(i.hint) })),
        ...shares.map((s) => ({ label: s.name, hint: t('Share'), href: `#/shares?sel=${encodeURIComponent(s.name)}`, icon: 'folder' })),
        ...users.map((u) => ({ label: u.name, hint: t('User'), href: `#/users?sel=${encodeURIComponent(u.name)}`, icon: 'users' })),
        ...volumes.map((v) => ({ label: v.name, hint: t('Volume'), href: `#/volumes?sel=${encodeURIComponent(v.name)}`, icon: 'database' })),
      ]
    } catch {}
  })

  let results = $derived(
    (query.trim()
      ? items.filter((i) => (i.label + ' ' + i.hint).toLowerCase().includes(query.trim().toLowerCase()))
      : items
    ).slice(0, 12),
  )
  $effect(() => {
    query
    active = 0
  })

  function go(item) {
    location.hash = item.href.slice(1)
    onClose()
  }
  function key(e) {
    if (e.key === 'Escape') onClose()
    else if (e.key === 'ArrowDown') { e.preventDefault(); active = Math.min(active + 1, results.length - 1) }
    else if (e.key === 'ArrowUp') { e.preventDefault(); active = Math.max(active - 1, 0) }
    else if (e.key === 'Enter' && results[active]) go(results[active])
  }
</script>

<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
<div class="backdrop palette-backdrop" onclick={(e) => e.target === e.currentTarget && onClose()}>
  <div class="card palette" role="dialog" aria-modal="true" aria-label={t('Search')}>
    <div class="palette-input">
      <Icon name="search" size={16} />
      <input bind:this={input} bind:value={query} onkeydown={key} placeholder={t('Search pages, shares, users, volumes…')}
        aria-label={t('Search')} aria-controls="palette-results" />
      <span class="kbd">Esc</span>
    </div>
    <ul id="palette-results" role="listbox">
      {#each results as r, i (r.href)}
        <li role="option" aria-selected={i === active}>
          <button class:on={i === active} onclick={() => go(r)} onmouseenter={() => (active = i)}>
            <Icon name={r.icon} size={15} /> {r.label} <span class="muted small">{r.hint}</span>
          </button>
        </li>
      {:else}
        <li class="muted small none">{t('Nothing found.')}</li>
      {/each}
    </ul>
  </div>
</div>

<style>
  .palette-backdrop { place-items: start center; padding-top: 12vh; }
  .palette { width: 100%; max-width: 560px; padding: 0; overflow: hidden; }
  .palette-input { display: flex; align-items: center; gap: 0.6rem; padding: 0.75rem 1rem; border-bottom: 1px solid var(--border); color: var(--muted); }
  .palette-input input { flex: 1; border: 0; background: none; padding: 0.2rem 0; font-size: 1rem; outline: none; }
  ul { list-style: none; margin: 0; padding: 0.4rem; max-height: 50vh; overflow-y: auto; }
  li button { width: 100%; display: flex; align-items: center; gap: 0.6rem; background: none; color: var(--text); text-align: left; padding: 0.5rem 0.65rem; }
  li button .small { margin-left: auto; }
  li button.on { background: var(--surface-3); }
  .none { padding: 0.75rem; }
</style>
