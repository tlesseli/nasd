<script>
  import { untrack } from 'svelte'

  // Create or edit a share. `share` is null for a new one.
  let { share: shareProp = null, volumes: volumesProp, users: usersProp, settings = {}, onSave, onCancel } = $props()

  // The form works on a snapshot taken when it opens: the page refreshing its
  // lists in the background must not reset what you're typing.
  const { share, volumes, users } = untrack(() => ({ share: shareProp, volumes: volumesProp, users: usersProp }))
  const editing = !!share
  let name = $state(share?.name ?? '')
  let volume = $state(share?.volume ?? volumes[0]?.name ?? '')
  let description = $state(share?.description ?? '')
  // Every user gets an entry: '' (no access), 'ro' or 'rw'.
  let access = $state(Object.fromEntries(users.map((u) => [u.name, share?.access?.[u.name] ?? ''])))
  let guestRead = $state(share?.guestRead ?? false)
  let recycleBin = $state(share?.recycleBin ?? true)
  let timeMachine = $state(share?.timeMachine ?? false)
  let rsyncRead = $state(share?.rsyncRead ?? false)
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
        name, volume, description, guestRead, recycleBin, rsyncRead, hidden: smb && hidden,
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

<form class="card create" onsubmit={submit}>
  <h2>{editing ? `Edit ${share.name}` : 'New share'}</h2>

  {#if !editing}
    <label>
      Name
      <!-- svelte-ignore a11y_autofocus -->
      <input bind:value={name} maxlength="32" autocomplete="off" spellcheck="false" placeholder="e.g. Photos" autofocus />
      <span class="hint">Letters, digits, '.', '-' and '_'. It's also the folder name on the volume.</span>
    </label>
    <label>
      Volume
      <select bind:value={volume}>
        {#each volumes as v (v.name)}
          <option value={v.name}>{v.name} · {v.mountpoint}</option>
        {/each}
      </select>
    </label>
  {/if}

  <label>
    Description <span class="hint">(optional, shown in some file browsers)</span>
    <input bind:value={description} maxlength="100" />
  </label>

  <fieldset>
    <legend>Who can access it</legend>
    {#if users.length}
      <table class="members access">
        <tbody>
          {#each users as u (u.name)}
            <tr>
              <td><strong>{u.name}</strong></td>
              <td class="right">
                <div class="segmented" role="radiogroup" aria-label="Access for {u.name}">
                  {#each [['', 'No access'], ['ro', 'Read only'], ['rw', 'Read & write']] as [val, label]}
                    <label class:on={access[u.name] === val}>
                      <input type="radio" bind:group={access[u.name]} value={val} />{label}
                    </label>
                  {/each}
                </div>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    {:else}
      <p class="muted small">No users yet. Create them on the Users page; until then only guest access is possible.</p>
    {/if}
    <label class="check">
      <input type="checkbox" bind:checked={guestRead} />
      <span><strong>Guests can read</strong> <span class="muted small">· anyone on your network can open it without a password (read only)</span></span>
    </label>
    {#if nobody}<p class="hint">Nobody has access yet: the share stays hidden until you give someone access.</p>{/if}
    <label>
      Allowed computers <span class="hint">(optional)</span>
      <input bind:value={allowedHosts} placeholder="e.g. 192.168.1.20, 192.168.1.0/24" spellcheck="false" />
      <span class="hint">IP addresses or networks. Empty = any computer on your network. Applies to SMB and rsync.</span>
    </label>
  </fieldset>

  <fieldset>
    <legend>Services for this share</legend>
    <label class="check">
      <input type="checkbox" bind:checked={smb} />
      <span><strong>SMB</strong> <span class="muted small">· open it from Windows, macOS, Linux and phones, with the access rights above
        {settings.smbDisabled ? '(the SMB service is off: switch it on above the share list)' : ''}</span></span>
    </label>
    {#if smb}
      <label class="check sub">
        <input type="checkbox" checked={!hidden} onchange={(e) => (hidden = !e.currentTarget.checked)} />
        <span><strong>Listed when browsing the network</strong> <span class="muted small">· in Windows Explorer and the macOS Finder. Unlisted, it still opens by typing its address.</span></span>
      </label>
      <label class="check sub">
        <input type="checkbox" bind:checked={timeMachine} />
        <span><strong>Time Machine</strong> <span class="muted small">· Macs can use this share as a backup disk (announced through Bonjour)</span></span>
      </label>
    {/if}
    <label class="check">
      <input type="checkbox" bind:checked={rsyncRead} />
      <span><strong>rsync (read only)</strong> <span class="muted small">· another NAS or computer can back this share up, without a password
        {settings.rsyncEnabled ? '' : '(the rsync service is off: switch it on above the share list)'}</span></span>
    </label>
    {#if !smb && !rsyncRead}<p class="hint">With no service, the share is kept but nobody can reach it over the network.</p>{/if}
  </fieldset>

  <fieldset>
    <legend>Options</legend>
    <label class="check">
      <input type="checkbox" bind:checked={recycleBin} />
      <span><strong>Recycle bin</strong> <span class="muted small">· files deleted over SMB are moved to a hidden <span class="mono">.recycle</span> folder instead of disappearing</span></span>
    </label>
  </fieldset>

  {#if error}<p class="error">{error}</p>{/if}
  <div class="actions">
    <button type="button" class="secondary" onclick={onCancel}>Cancel</button>
    <button type="submit" disabled={busy || (!editing && (!name || !volume))}>{busy ? 'Saving…' : editing ? 'Save' : 'Create share'}</button>
  </div>
</form>
