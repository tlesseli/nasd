<script>
  import { onMount } from 'svelte'
  import { api } from '../api.js'
  import { t, N_ } from '../i18n.svelte.js'

  // The NAS's own certificate authority: installed once on a computer, the
  // browser trusts the NAS with no warning.
  let info = $state(null)
  let error = $state('')
  let notice = $state('')
  let os = $state(guessOS())

  function guessOS() {
    const ua = navigator.userAgent
    if (/Firefox\//.test(ua)) return 'firefox'
    if (/iPhone|iPad/.test(ua)) return 'ios'
    if (/Android/.test(ua)) return 'android'
    if (/Mac OS X/.test(ua)) return 'mac'
    if (/Windows/.test(ua)) return 'windows'
    return 'linux'
  }
  onMount(async () => {
    try {
      info = await api.get('/api/certificate')
    } catch (e) {
      error = e.message
    }
  })

  async function showOnDisplay() {
    error = notice = ''
    try {
      await api.post('/api/certificate/show')
      notice = t('The first 16 characters are on the front display for 10 minutes.')
    } catch (e) {
      error = e.message
    }
  }

  const day = (d) => new Date(d).toLocaleDateString([], { day: 'numeric', month: 'long', year: 'numeric' })
  const systems = [
    ['windows', 'Windows'], ['mac', 'Mac'], ['linux', 'Linux'], ['firefox', 'Firefox'], ['ios', 'iPhone / iPad'], ['android', 'Android'],
  ]
  const steps = {
    windows: [
      N_('Open the downloaded file and click Install Certificate.'),
      N_('Choose Current User, then "Place all certificates in the following store", Browse, and Trusted Root Certification Authorities.'),
      N_('Finish, and answer Yes to the security warning. Chrome and Edge use it at once; Firefox has its own list (see Firefox).'),
    ],
    mac: [
      N_('Open the downloaded file: Keychain Access adds it to your login keychain.'),
      N_('Double-click the certificate (named after your NAS), open Trust, and choose Always Trust for "When using this certificate".'),
      N_('Close the window and confirm with your Mac password. Safari and Chrome use it at once.'),
    ],
    linux: [
      N_('Chrome: Settings, Privacy and security, Security, Manage certificates, Authorities, Import. Tick "Trust this certificate for identifying websites".'),
      N_('For the whole system (Debian, Ubuntu): sudo cp the file to /usr/local/share/ca-certificates/ and run sudo update-ca-certificates.'),
    ],
    firefox: [
      N_('Settings, Privacy & Security, Certificates: View Certificates.'),
      N_('Authorities tab, Import, choose the downloaded file, and tick "Trust this CA to identify websites".'),
    ],
    ios: [
      N_('Open this page in Safari and download the certificate; allow the configuration profile.'),
      N_('Settings, Profile Downloaded, Install.'),
      N_('Then Settings, General, About, Certificate Trust Settings: switch it on for this certificate.'),
    ],
    android: [
      N_('Download the certificate.'),
      N_('Settings, Security (or Security & privacy), Encryption & credentials, Install a certificate, CA certificate, and choose the file.'),
    ],
  }
</script>

{#if error}<p class="error">{error}</p>{/if}
{#if notice}<p class="notice">{notice}</p>{/if}

{#if info}
  <section class="card create">
    <h2>{t('Stop the browser warning')}</h2>
    <p class="hint">{t('Your browser warns about this NAS because no public authority signed its certificate. Install the NAS’s own certificate once on each computer: the warning goes away for good, and a fake NAS would get a warning you would notice.')}</p>
    <p class="hint">{t('It only vouches for this NAS (its names and local addresses), never for any other site.')}</p>
    <div class="add">
      <a class="button" href="/api/certificate/ca.crt" download>{t('Download the certificate')}</a>
      <span class="muted small mono">{info.caName}</span>
    </div>

    <h3>{t('Install it')}</h3>
    <div class="tabs" role="tablist">
      {#each systems as [id, label] (id)}
        <button type="button" role="tab" class="tab" class:on={os === id} aria-selected={os === id} onclick={() => (os = id)}>{label}</button>
      {/each}
    </div>
    <ol class="steps">
      {#each steps[os] as s, i (i)}<li>{t(s)}</li>{/each}
      <li>{t('Reload this page: the address bar shows a padlock, with no warning.')}</li>
    </ol>

    <h3>{t('Check it')}</h3>
    <p class="hint">{t('When you install it, your system shows the certificate’s SHA-256 fingerprint. It must start like the one on the NAS’s front display: that proves the file came from your NAS, not from someone in between.')}</p>
    <p class="mono small fingerprint"><strong>{info.fingerprint.slice(0, 23)}</strong>{info.fingerprint.slice(23)}</p>
    <div class="add"><button class="secondary" onclick={showOnDisplay}>{t('Show on the front display')}</button></div>
  </section>

  <section class="card create">
    <h2>{t('Server certificate')}</h2>
    <dl class="facts small">
      <dt>{t('Valid for')}</dt><dd class="mono">{info.names.join(', ')}</dd>
      <dt>{t('Valid until')}</dt><dd>{day(info.expires)} <span class="muted">· {t('renewed automatically a month before, or when the NAS’s address or name changes')}</span></dd>
      <dt>{t('Authority valid until')}</dt><dd>{day(info.caExpires)}</dd>
    </dl>
    <p class="hint">{t('Renaming the NAS makes a new authority: install the certificate again on your computers.')}</p>
  </section>
{/if}

<style>
  h2, h3 { display: flex; align-items: center; gap: 0.5rem; }
  .tabs { display: flex; flex-wrap: wrap; gap: 0.3rem; }
  .tab { background: var(--surface-3); color: var(--text); border: 1px solid var(--border); padding: 0.3rem 0.7rem; font-size: 0.85rem; }
  .tab.on { background: var(--primary-bg); color: var(--on-primary); border-color: transparent; }
  .steps { margin: 0.4rem 0 0; padding-left: 1.3rem; display: flex; flex-direction: column; gap: 0.35rem; }
  .fingerprint { word-break: break-all; margin: 0.3rem 0; }
  .facts { display: grid; grid-template-columns: max-content 1fr; gap: 0.35rem 1rem; margin: 0; }
  .facts dt { color: var(--muted); }
  .facts dd { margin: 0; word-break: break-word; }
  a.button { display: inline-block; font-weight: 500; padding: 0.5rem 0.95rem; border-radius: var(--radius-sm); background: var(--primary-bg); color: var(--on-primary); text-decoration: none; }
  a.button:hover { filter: brightness(0.92); }
</style>
