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

  function save() {
    msg = null
    const keys = text.split('\n').map((k) => k.trim()).filter(Boolean)
    dialog = {
      title: t('Change the SSH keys?'),
      message: keys.length
        ? t('{n} key(s) will be able to log in as {user} over SSH, with full control of the system.', { n: keys.length, user: info.user })
        : t('No key will be allowed: SSH login as {user} will be impossible.', { user: info.user }),
      confirmLabel: t('Save keys'),
      danger: false,
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
