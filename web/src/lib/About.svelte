<script>
  import { onMount } from 'svelte'
  import { api } from '../api.js'
  import { t } from '../i18n.svelte.js'
  import Copyright from './Copyright.svelte'

  // Why this software exists, and where to send bugs.
  /* global __APP_VERSION__ */
  let { hostname } = $props()
  let build = $state('')
  onMount(async () => {
    build = (await api.get('/api/system').catch(() => null))?.version ?? ''
  })
  const CONTACT = 'tanguy@ulixes-consulting.be'
  const COFFEE = 'https://buymeacoffee.com/tanguylesseli'
  let mailto = $derived(`mailto:${CONTACT}?subject=${encodeURIComponent(`nasd ${build || __APP_VERSION__} – bug report (${hostname})`)}`)
</script>

<section class="card about">
  <div class="card-head"><div><h2>{t('About')}</h2><div class="muted small">nasd {__APP_VERSION__}{build ? ` · ${t('build')} ${build}` : ''}</div></div></div>

  <h3>{t('Why this version')}</h3>
  <p>{t('The ReadyNAS shipped with RAIDiator, an operating system Netgear stopped updating years ago. It only speaks the old SMB1 protocol, which today’s computers refuse for security reasons, and its web interface no longer opens in a modern browser.')}</p>
  <p>{t('nasd replaces it with a current Debian system and a management interface written for this hardware: RAID volumes (including the ones RAIDiator made), shares over SMB, FTP and rsync, users and quotas, backups and a cloud archive, alerts, power management, and the front panel (display, disk lights, Backup button).')}</p>
  <p>{t('It is built step by step with its owner and tested on the real machine, a ReadyNAS Ultra 4.')}</p>

  <h3>{t('Found a bug? Have an idea?')}</h3>
  <p>{t('Please send it to {email}: what you did, what you expected, and what happened instead. A screenshot helps. The version above is added to the subject for you.', { email: CONTACT })}</p>
  <p><a class="button-like" href={mailto}>{t('Report a bug')}</a></p>

  <h3>{t('Support this project')}</h3>
  <p>{t('nasd is free software: you may use, share and change it. If it is useful to you and you want to say thanks, you can buy its author a coffee.')}</p>
  <p><a class="button-like coffee" href={COFFEE} target="_blank" rel="noopener noreferrer">☕ {t('Buy me a coffee')}</a></p>

  <Copyright />
</section>

<style>
  .about h3 { margin: 1.1rem 0 0.3rem; }

  .button-like { display: inline-block; padding: 0.5rem 0.95rem; border-radius: var(--radius-sm); background: var(--primary-bg); color: var(--on-primary);
    text-decoration: none; font-weight: 500; }
  .button-like:hover { text-decoration: none; filter: brightness(0.92); }
  .coffee { background: #ffdd00; color: #1a1205; }
</style>
