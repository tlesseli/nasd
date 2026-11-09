<script>
  import { untrack } from 'svelte'
  import { t } from '../i18n.svelte.js'

  // Create or edit a share. `share` is null for a new one.
  // bare: rendered inside another card (the Shares side panel).
  let { share: shareProp = null, preset = null, volumes: volumesProp, users: usersProp, settings = {}, bare = false, onSave, onCancel, onDelete } = $props()

  // The form works on a snapshot taken when it opens: the page refreshing its
  // lists in the background must not reset what you're typing.
  const { share, volumes, users } = untrack(() => ({ share: shareProp, volumes: volumesProp, users: usersProp }))
  const editing = !!share
  // preset: a new share for a folder that already exists ({name, volume}).
  let name = $state(share?.name ?? untrack(() => preset?.name) ?? '')
  let volume = $state(share?.volume ?? untrack(() => preset?.volume) ?? volumes[0]?.name ?? '')
  let description = $state(share?.description ?? '')
  // Every user gets an entry: '' (no access), 'ro' or 'rw'.
  let access = $state(Object.fromEntries(users.map((u) => [u.name, share?.access?.[u.name] ?? ''])))
  let guestRead = $state(share?.guestRead ?? false)
  let recycleBin = $state(share?.recycleBin ?? true)
  let timeMachine = $state(share?.timeMachine ?? false)
  let rsync = $state(share?.rsync ?? '')
  let rsyncUser = $state(share?.rsyncUser ?? '')
  let rsyncPassword = $state('')
  let ftp = $state(share?.ftp ?? false)
  let hidden = $state(share?.hidden ?? false)
  let smb = $state(!(share?.smbDisabled ?? false))
  let allowedHosts = $state((share?.allowedHosts ?? []).join(', '))
  let busy = $state(false)
  let error = $state('')

  let nobody = $derived(!guestRead && Object.values(access).every((a) => !a))

  async function submit(event) {
    event.preventDefault()
    busy = true
    error = ''
    try {
      await onSave({
        name, volume, description, guestRead, recycleBin, hidden: smb && hidden, ftp,
        rsync, rsyncUser: rsync ? rsyncUser : '', rsyncPassword: rsync ? rsyncPassword : '',
        smbDisabled: !smb, timeMachine: smb && timeMachine,
        allowedHosts: allowedHosts.split(/[\s,]+/).filter(Boolean),
        access: Object.fromEntries(Object.entries(access).filter(([, a]) => a)),
      })
    } catch (e) {
      error = e.message
      busy = false
    }
  }
</script>

<form class="create" class:card={!bare} onsubmit={submit}>
  {#if !bare}<h2>{editing ? t('Edit {name}', { name: share.name }) : t('New share')}</h2>{/if}

  {#if !editing}
    <label>
      {t('Name')}
      <!-- svelte-ignore a11y_autofocus -->
      <input bind:value={name} maxlength="32" autocomplete="off" spellcheck="false" placeholder={t('e.g. Photos')} autofocus />
      <span class="hint">{t("Letters, digits, '.', '-' and '_'. It's also the folder name on the volume.")}</span>
    </label>
    <label>
      {t('Volume')}
      <select bind:value={volume}>
        {#each volumes as v (v.name)}
          <option value={v.name}>{v.name} · {v.mountpoint}</option>
        {/each}
      </select>
    </label>
  {/if}

  <label>
    {t('Description')} <span class="hint">{t('(optional, shown in some file browsers)')}</span>
    <input bind:value={description} maxlength="100" />
  </label>

  <fieldset>
    <legend>{t('Who can access it')}</legend>
    {#if users.length}
      <div class="access-list">
        {#each users as u (u.name)}
          <div class="access-row">
            <span class="avatar">{u.name.slice(0, 2).toUpperCase()}</span>
            <div class="who"><div>{u.name}</div><div class="muted small">{t('User')}</div></div>
            <select bind:value={access[u.name]} aria-label={t('Access for {name}', { name: u.name })}>
              <option value="">{t('No access')}</option>
              <option value="ro">{t('Read only')}</option>
              <option value="rw">{t('Read / write')}</option>
            </select>
          </div>
        {/each}
      </div>
    {:else}
      <p class="muted small">{t('No users yet. Create them on the Users page; until then only guest access is possible.')}</p>
    {/if}
    <label class="check">
      <input type="checkbox" bind:checked={guestRead} />
      <span><strong>{t('Guests can read')}</strong> <span class="muted small">· {t('anyone on your network can open it without a password (read only)')}</span></span>
    </label>
    {#if nobody}<p class="hint">{t('Nobody has access yet: the share stays hidden until you give someone access.')}</p>{/if}
    <label>
      {t('Allowed computers')} <span class="hint">{t('(optional)')}</span>
      <input bind:value={allowedHosts} placeholder={t('e.g. 192.168.1.20, 192.168.1.0/24')} spellcheck="false" />
      <span class="hint">{t('IP addresses or networks. Empty = any computer on your network. Applies to SMB and rsync.')}</span>
    </label>
  </fieldset>

  <fieldset>
    <legend>{t('Services for this share')}</legend>
    <label class="check">
      <input type="checkbox" bind:checked={smb} />
      <span><strong>SMB</strong> <span class="muted small">· {t('open it from Windows, macOS, Linux and phones, with the access rights above')}
        {settings.smbDisabled ? t('(the SMB service is off: switch it on above the share list)') : ''}</span></span>
    </label>
    {#if smb}
      <label class="check sub">
        <input type="checkbox" checked={!hidden} onchange={(e) => (hidden = !e.currentTarget.checked)} />
        <span><strong>{t('Listed when browsing the network')}</strong> <span class="muted small">· {t('in Windows Explorer and the macOS Finder. Unlisted, it still opens by typing its address.')}</span></span>
      </label>
      <label class="check sub">
        <input type="checkbox" bind:checked={timeMachine} />
        <span><strong>Time Machine</strong> <span class="muted small">· {t('Macs can use this share as a backup disk (announced through Bonjour)')}</span></span>
      </label>
    {/if}
    <label class="check">
      <input type="checkbox" bind:checked={ftp} />
      <span><strong>FTP</strong> <span class="muted small">· {t('users open it with an FTP client, with the same rights as above')}
        {settings.ftpEnabled ? '' : t('(the FTP service is off: System → Services)')}</span></span>
    </label>
    <div class="check-row">
      <span><strong>rsync</strong> <span class="muted small">· {t('another NAS or computer can copy from (read) or to (read & write) this share')}
        {settings.rsyncEnabled ? '' : t('(the rsync service is off: System → Services)')}</span></span>
      <select bind:value={rsync} aria-label={t('rsync access')}>
        <option value="">{t('Off')}</option>
        <option value="ro">{t('Read only')}</option>
        <option value="rw">{t('Read & write')}</option>
      </select>
    </div>
    {#if rsync}
      <div class="two-cols sub-fields">
        <label>{t('rsync user')} {rsync === 'ro' ? t('(optional)') : ''} <input bind:value={rsyncUser} autocomplete="off" spellcheck="false" /></label>
        <label>{t('rsync password')} {share?.rsyncUser && rsyncUser === share.rsyncUser ? t('(empty = keep)') : ''}
          <input type="password" bind:value={rsyncPassword} autocomplete="new-password" /></label>
      </div>
      <p class="hint">{t('rsync has its own login, separate from NAS users.')} {rsync === 'rw' ? t('Writing requires it.') : t('Without one, anyone on your network can read this share over rsync.')}</p>
    {/if}
    {#if !smb && !rsync && !ftp}<p class="hint">{t('With no service, the share is kept but nobody can reach it over the network.')}</p>{/if}
  </fieldset>

  <fieldset>
    <legend>{t('Options')}</legend>
    <label class="check">
      <input type="checkbox" bind:checked={recycleBin} />
      <span><strong>{t('Recycle bin')}</strong> <span class="muted small">· {t('files deleted over SMB are moved to a hidden')} <span class="mono">.recycle</span> {t('folder instead of disappearing')}</span></span>
    </label>
  </fieldset>

  {#if error}<p class="error">{error}</p>{/if}
  <div class="actions">
    {#if editing && onDelete}<button type="button" class="secondary danger-text" onclick={onDelete}>{t('Delete')}</button><span class="spacer"></span>{/if}
    <button type="button" class="secondary" onclick={onCancel}>{bare && editing ? t('Close') : t('Cancel')}</button>
    <button type="submit" disabled={busy || (!editing && (!name || !volume))}>{busy ? t('Saving…') : editing ? t('Save') : t('Create share')}</button>
  </div>
</form>

<style>
  .access-list { display: flex; flex-direction: column; gap: 0.5rem; }
  .access-row { display: flex; align-items: center; gap: 0.65rem; }
  .access-row .who { flex: 1; min-width: 0; }
  .access-row select { width: 9.5rem; }
</style>
