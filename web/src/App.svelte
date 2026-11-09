<script>
  import { onMount } from 'svelte'
  import { api, formatBytes } from './api.js'
  import Icon from './ui/Icon.svelte'
  import Search from './ui/Search.svelte'
  import Login from './lib/Login.svelte'
  import Dashboard from './lib/dashboard/Dashboard.svelte'
  import Copyright from './lib/Copyright.svelte'
  import Volumes from './lib/Volumes.svelte'
  import Shares from './lib/Shares.svelte'
  import Users from './lib/Users.svelte'
  import Backups from './lib/Backups.svelte'
  import System from './lib/System.svelte'
  import { t, N_ } from './i18n.svelte.js'

  // 'loading' | 'setup' | 'login' | 'app'
  let screen = $state('loading')
  let user = $state(null)
  let bootError = $state('')
  let sys = $state(null)
  let volumes = $state([])
  let problems = $state([])
  let searchOpen = $state(false)
  let menuOpen = $state(false)
  let codeRequired = $state(false) // first start: setup code needed

  // Hash router: #/shares?sel=Photos  ->  path "/shares", params {sel: "Photos"}
  function parseHash() {
    const [path, query] = (location.hash.slice(1) || '/').split('?')
    return { path, params: Object.fromEntries(new URLSearchParams(query ?? '')) }
  }
  let route = $state(parseHash())

  const pages = [
    { path: '/', label: N_('Overview'), icon: 'home' },
    { path: '/volumes', label: N_('Volumes & Disks'), icon: 'database', badge: true },
    { path: '/shares', label: N_('Shares'), icon: 'folder' },
    { path: '/users', label: N_('Users'), icon: 'users' },
    { path: '/backups', label: N_('Backups'), icon: 'archive' },
    { path: '/system', label: N_('System Settings'), icon: 'settings' },
  ]
  const section = (p) => '/' + (p.split('/')[1] ?? '')

  // Shared status (sidebar footer, badge), refreshed every 20 s.
  async function refreshStatus() {
    try {
      ;[sys, volumes, problems] = await Promise.all([api.get('/api/system'), api.get('/api/volumes'), api.get('/api/attention')])
    } catch (e) {
      if (e.status === 401) screen = 'login'
    }
  }

  onMount(() => {
    const onHash = () => {
      route = parseHash()
      menuOpen = false
    }
    const onKey = (e) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        searchOpen = !searchOpen
      }
    }
    window.addEventListener('hashchange', onHash)
    window.addEventListener('keydown', onKey)
    ;(async () => {
      try {
        const { needsSetup, codeRequired: c } = await api.get('/api/setup')
        codeRequired = !!c
        if (needsSetup) return (screen = 'setup')
        onAuthenticated((await api.get('/api/me')).username)
      } catch (e) {
        if (e.status === 401) screen = 'login'
        else bootError = e.message
      }
    })()
    const timer = setInterval(() => screen === 'app' && refreshStatus(), 20_000)
    return () => {
      window.removeEventListener('hashchange', onHash)
      window.removeEventListener('keydown', onKey)
      clearInterval(timer)
    }
  })

  async function onAuthenticated(name) {
    user = name
    screen = 'app'
    refreshStatus()
  }

  async function logout() {
    await api.post('/api/logout')
    user = null
    screen = 'login'
  }

  const onSessionLost = () => (screen = 'login')
  const hostname = $derived(sys?.hostname ?? 'nas')
  const used = $derived(volumes.reduce((n, v) => n + (v.usedBytes || 0), 0))
  const total = $derived(volumes.reduce((n, v) => n + (v.totalBytes || 0), 0))
  const storageProblems = $derived(problems.filter((p) => /^(raid|disk|volume|quota|scrub)/.test(p.kind)))
  const days = $derived(sys ? Math.floor(sys.uptimeSeconds / 86400) : 0)
</script>

{#if bootError}
  <main class="center"><p class="error">{t('Cannot reach the NAS: {error}', { error: bootError })}</p></main>
{:else if screen === 'setup' || screen === 'login'}
  <Login setup={screen === 'setup'} {codeRequired} {onAuthenticated} />
{:else if screen === 'app'}
  <div class="shell">
    <aside class="sidebar" class:open={menuOpen} aria-label={t('Navigation')}>
      <div class="brand">
        <div class="brand-logo"><Icon name="server" size={18} /></div>
        <div>
          <div class="brand-name">{hostname}</div>
          <div class="brand-host">{hostname}.local</div>
        </div>
      </div>
      <button class="search-btn" onclick={() => (searchOpen = true)}>
        <Icon name="search" size={15} /> {t('Search…')} <span class="kbd">Ctrl K</span>
      </button>
      <div class="nav-label">{t('MANAGE')}</div>
      <nav class="nav">
        {#each pages as p (p.path)}
          <a href="#{p.path}" class:active={section(route.path) === p.path} aria-current={section(route.path) === p.path ? 'page' : undefined}>
            <Icon name={p.icon} size={17} /> {t(p.label)}
            {#if p.badge && storageProblems.length}
              <span class="nav-badge" class:bad={storageProblems.some((x) => x.severity === 'critical')}
                title={t('{n} problem(s)', { n: storageProblems.length })}>{storageProblems.length}</span>
            {/if}
          </a>
        {/each}
      </nav>

      <div class="status-card">
        <div class="online"><span class="dot" class:warn={problems.length} class:bad={problems.some((x) => x.severity === 'critical')}></span>
          {days === 1 ? t('Online · {n} day', { n: days }) : t('Online · {n} days', { n: days })}</div>
        {#if total}
          <span class="meter"><span style="width: {(100 * used) / total}%"></span></span>
          <span class="muted">{t('{used} of {total}', { used: formatBytes(used), total: formatBytes(total) })} · nasd {sys?.version ?? ''}</span>
        {:else}
          <span class="muted">{t('No volume yet')} · nasd {sys?.version ?? ''}</span>
        {/if}
      </div>
      <div class="account">
        <span class="avatar">{(user ?? 'ad').slice(0, 2).toUpperCase()}</span>
        <div><div>{user}</div><div class="muted small">{t('Administrator')}</div></div>
        <span class="spacer"></span>
        <button class="icon-btn" onclick={logout} title={t('Sign out')} aria-label={t('Sign out')}><Icon name="logout" size={16} /></button>
      </div>
      <Copyright />
    </aside>

    <div class="main">
      <div class="mobile-bar">
        <button class="icon-btn" onclick={() => (menuOpen = !menuOpen)} aria-label={t('Menu')}><Icon name="menu" size={20} /></button>
        <strong>{hostname}</strong>
        <span class="spacer"></span>
        <button class="icon-btn" onclick={() => (searchOpen = true)} aria-label={t('Search')}><Icon name="search" size={18} /></button>
      </div>
      {#if section(route.path) === '/volumes'}
        <Volumes {onSessionLost} params={route.params} />
      {:else if section(route.path) === '/shares'}
        <Shares {hostname} {onSessionLost} params={route.params} />
      {:else if section(route.path) === '/users'}
        <Users {onSessionLost} />
      {:else if section(route.path) === '/backups'}
        <Backups {onSessionLost} />
      {:else if section(route.path) === '/system'}
        <System {hostname} {onSessionLost} sectionId={route.path.split('/')[2] ?? 'general'} />
      {:else}
        <Dashboard {user} {onSessionLost} {problems} />
      {/if}
    </div>
  </div>
  {#if searchOpen}<Search onClose={() => (searchOpen = false)} />{/if}
{/if}
