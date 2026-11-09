<script>
  import { formatBytes } from '../api.js'
  import { t, N_ } from '../i18n.svelte.js'

  import { untrack } from 'svelte'

  let { freeDisks, onCreate, onCancel } = $props()
  // Defaults computed once, when the form opens.
  const initialDisks = untrack(() => freeDisks)

  // Mirrors volumes.Levels in Go. The server re-checks everything anyway.
  const levels = [
    { id: 'raid5', label: 'RAID 5', min: 3, usable: (n) => n - 1, survives: N_('1 disk'),
      desc: N_('Data and parity spread over all disks. Best balance of space and safety for 3–4 disks.') },
    { id: 'raid6', label: 'RAID 6', min: 4, usable: (n) => n - 2, survives: N_('2 disks'),
      desc: N_('Double parity. Safest choice with 4 disks, at the cost of two disks of space.') },
    { id: 'raid10', label: 'RAID 10', min: 4, even: true, usable: (n) => Math.floor(n / 2), survives: N_('1 disk (sometimes 2)'),
      desc: N_('Mirrored pairs, striped. Fast, rebuilds quickly, half the space.') },
    { id: 'raid1', label: 'RAID 1', min: 2, usable: () => 1, survives: N_('all but 1 disk'),
      desc: N_('Every disk holds a full copy. Simple and robust, the space of one disk.') },
    { id: 'raid0', label: 'RAID 0', min: 2, usable: (n) => n, survives: N_('nothing'),
      desc: N_('No redundancy: one failed disk loses everything. Only for scratch data.') },
  ]

  let name = $state('')
  let level = $state(initialDisks.length >= 3 ? 'raid5' : 'raid1')
  let filesystem = $state('ext4')
  let selected = $state(initialDisks.map((d) => d.name))
  let understood = $state(false)
  let busy = $state(false)
  let error = $state('')

  let lvl = $derived(levels.find((l) => l.id === level))
  let chosen = $derived(freeDisks.filter((d) => selected.includes(d.name)))
  let smallest = $derived(chosen.length ? Math.min(...chosen.map((d) => d.sizeBytes)) : 0)
  let countError = $derived(
    chosen.length < lvl.min ? t('{level} needs at least {n} disks.', { level: lvl.label, n: lvl.min })
    : lvl.even && chosen.length % 2 ? t('{level} needs an even number of disks.', { level: lvl.label })
    : '',
  )
  let nameOk = $derived(/^[a-z][a-z0-9_-]{0,15}$/.test(name))
  let canSubmit = $derived(nameOk && !countError && understood && !busy)
  let erasedParts = $derived(t('I understand that {data} will be erased.').split('{data}'))

  async function submit(event) {
    event.preventDefault()
    busy = true
    error = ''
    try {
      await onCreate({ name, level, filesystem, disks: chosen.map((d) => d.name) })
    } catch (e) {
      error = e.message
      busy = false
    }
  }
</script>

<form class="card create" onsubmit={submit}>
  <h2>{t('New volume')}</h2>

  <label>
    {t('Name')}
    <input bind:value={name} placeholder={t('e.g. data')} maxlength="16" autocomplete="off" spellcheck="false" />
    <span class="hint">{t('Lowercase letters, digits, - and _.')} {t('Mounted at')} <span class="mono">/data/{name || '…'}</span></span>
  </label>

  <fieldset>
    <legend>{t('Disks')}</legend>
    {#each freeDisks as d (d.name)}
      <label class="check">
        <input type="checkbox" bind:group={selected} value={d.name} />
        <span><strong>{d.name}</strong> · {formatBytes(d.sizeBytes)} · <span class="muted">{d.model} {d.serial}</span></span>
      </label>
    {:else}
      <p class="muted">{t('No free disks. Erase a disk or delete a volume first.')}</p>
    {/each}
  </fieldset>

  <fieldset>
    <legend>{t('Protection')}</legend>
    <div class="levels">
      {#each levels as l (l.id)}
        <label class="level" class:disabled={chosen.length < l.min}>
          <input type="radio" bind:group={level} value={l.id} />
          <span>
            <strong>{l.label}</strong> <span class="muted small">· {t('survives {what}', { what: t(l.survives) })}</span><br />
            <span class="muted small">{t(l.desc)}</span>
          </span>
        </label>
      {/each}
    </div>
  </fieldset>

  <label>
    {t('Filesystem')}
    <select bind:value={filesystem}>
      <option value="ext4">{t('ext4: mature and simple (recommended)')}</option>
      <option value="btrfs">{t('btrfs: snapshots and checksums (used later for snapshots)')}</option>
    </select>
  </label>

  <div class="summary">
    {#if countError}
      <span class="error">{countError}</span>
    {:else}
      {t('Usable space:')} <strong>{formatBytes(smallest * lvl.usable(chosen.length))}</strong>
      <span class="muted">{t('of {size} raw', { size: formatBytes(smallest * chosen.length) })}</span>
    {/if}
  </div>

  <label class="check warning">
    <input type="checkbox" bind:checked={understood} />
    <span>{erasedParts[0]}<strong>{t('all data on {disks}', { disks: chosen.map((d) => d.name).join(', ') || t('the selected disks') })}</strong>{erasedParts[1] ?? ''}</span>
  </label>

  {#if error}<p class="error">{error}</p>{/if}
  <div class="actions">
    <button type="button" class="secondary" onclick={onCancel}>{t('Cancel')}</button>
    <button type="submit" disabled={!canSubmit}>{busy ? t('Starting…') : t('Create volume')}</button>
  </div>
</form>
