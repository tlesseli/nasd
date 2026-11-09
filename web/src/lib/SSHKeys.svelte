<script>
  import { onMount } from 'svelte'
  import { api } from '../api.js'
  import { t } from '../i18n.svelte.js'
  import ConfirmDialog from './ConfirmDialog.svelte'

  // Public SSH keys allowed to log in as the system's administrator account
  // (with sudo). Saving needs the admin password.
  let info = $state(null)
  let text = $state('')
  let msg = $state(null)
  let dialog = $state(null)

  async function load() {
    info = await api.get('/api/ssh/keys').catch((e) => ({ error: e.message }))
    text = (info.keys ?? []).join('\n')
  }
  onMount(load)

  // A key's label: its comment (the last word), e.g. "nas-admin".
  const label = (k) => k.split(/\s+/)[2] || k.split(/\s+/)[1]?.slice(0, 16) + '…'

  function save() {
    msg = null
    const keys = text.split('\n').map((k) => k.trim()).filter(Boolean)
    if (!keys.length) return (msg = { ok: false, text: t('At least one key must stay: without any, nobody could log in over SSH.') })
    // Keys allowed now that would be removed: say which, and ask to confirm.
    const norm = (k) => k.split(/\s+/).slice(0, 2).join(' ')
    const removed = (info.keys ?? []).filter((k) => !keys.some((n) => norm(n) === norm(k)))
    const lines = [t('{n} key(s) will be able to log in as {user} over SSH, with full control of the system.', { n: keys.length, user: info.user })]
    if (removed.length) {
      lines.push(t('Removed: {keys}. Whoever uses it can no longer log in over SSH.', { keys: removed.map(label).join(', ') }))
      if (removed.some((k) => / nas-admin$/.test(k))) lines.push(t('nas-admin is the key used to install and update this NAS (image/id_ed25519 on your computer): keep it unless you replaced it.'))
    }
    dialog = {
      title: t('Change the SSH keys?'),
      message: lines.join(' '),
      expected: removed.length ? removed.map(label).join(', ') : '',
      confirmLabel: t('Save keys'),
      danger: removed.length > 0,
      askPassword: true,
      onConfirm: async (password) => {
        await api.post('/api/ssh/keys', { keys, password })
        dialog = null
        msg = { ok: true, text: t('SSH keys saved.') }
        load()
      },
    }
  }
</script>

<section class="card">
  <div class="card-head"><div><h2>{t('Administrator SSH access')}</h2>
    <div class="muted small">{t('Public keys allowed to log in as the system administrator (with sudo). One per line, e.g. the content of ~/.ssh/id_ed25519.pub on your computer. Passwords are never accepted over SSH.')}</div></div></div>
  {#if info?.error}
    <p class="muted small">{info.error}</p>
  {:else if info}
    <textarea bind:value={text} rows="4" spellcheck="false" class="mono keys" placeholder="ssh-ed25519 AAAA… you@laptop"></textarea>
    {#if msg}<p class={msg.ok ? 'good-text small' : 'error small'}>{msg.text}</p>{/if}
    <div class="actions"><button class="secondary" onclick={save}>{t('Save keys')}</button></div>
  {/if}
</section>

{#if dialog}<ConfirmDialog {...dialog} onCancel={() => (dialog = null)} />{/if}

<style>
  .keys { width: 100%; font-size: 0.8rem; margin: 0.4rem 0; }
</style>
