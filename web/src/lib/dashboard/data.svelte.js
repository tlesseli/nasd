// Live data shared by the dashboard styles (Control panel, Neon): one object
// whose fields refresh on their own; start() returns the cleanup function.
import { api } from '../../api.js'

export const BAYS = 4 // the ReadyNAS Ultra 4 has four drive bays

export function createDashboardData(onSessionLost) {
  const d = $state({
    sys: null, volumes: [], disks: [], services: [], backups: [], sensors: [], problems: [],
    hardware: null, network: null, metrics: [], loaded: false, error: '',
  })

  async function slow() {
    try {
      const [sys, volumes, disks, services, backups, sensors, problems, hardware, network] = await Promise.all([
        api.get('/api/system'), api.get('/api/volumes'), api.get('/api/disks'), api.get('/api/services'),
        api.get('/api/backups'), api.get('/api/sensors'), api.get('/api/attention'),
        api.get('/api/hardware').catch(() => null), api.get('/api/network').catch(() => null),
      ])
      Object.assign(d, { sys, volumes, disks, services, backups, sensors, problems, hardware, network: network?.info ?? null, loaded: true, error: '' })
    } catch (e) {
      if (e.status === 401) onSessionLost?.()
      else d.error = e.message
    }
  }
  async function fast() {
    try {
      d.metrics = (await api.get('/api/metrics?range=1h')).points
    } catch {}
  }

  function start() {
    slow()
    fast()
    const a = setInterval(slow, 15_000)
    const b = setInterval(fast, 5_000)
    return () => (clearInterval(a), clearInterval(b))
  }
  return { d, start, refresh: slow }
}

// ---- helpers shared by the styles

export const pools = (volumes) => volumes.filter((v) => !v.system)

export function latest(metrics) {
  return metrics[metrics.length - 1] ?? null
}

export function cpuTemp(sensors) {
  return sensors.filter((s) => s.kind === 'temp' && /coretemp|k10temp|cpu/i.test(s.chip + s.label)).reduce((m, s) => Math.max(m, s.value), 0)
}

export function memPercent(sys) {
  if (!sys?.memTotalBytes) return 0
  return (100 * (sys.memTotalBytes - sys.memAvailableBytes)) / sys.memTotalBytes
}

export function diskState(disk) {
  const h = disk?.health
  if (!disk) return 'empty'
  if (!h || !h.supported) return 'unknown'
  if (!h.passed) return 'fault'
  if (h.reallocatedSectors + h.pendingSectors + h.uncorrectableSectors > 0) return 'warn'
  return 'ok'
}

export function bays(disks) {
  return Array.from({ length: BAYS }, (_, i) => ({ n: i + 1, disk: disks.find((x) => x.bay === i + 1) }))
}

export function uptimeParts(seconds = 0) {
  const s = Math.max(0, Math.floor(seconds))
  return { d: Math.floor(s / 86400), h: Math.floor((s % 86400) / 3600), m: Math.floor((s % 3600) / 60), s: s % 60 }
}

// Alarm states derived from the live data (the Control panel's annunciator,
// the Neon status): true = lamp lit.
export function alarms(d) {
  const ps = pools(d.volumes)
  const kinds = new Set(d.problems.map((p) => p.kind))
  const ups = d.services.find((s) => s.unit === 'ups')
  const hottest = d.disks.reduce((m, x) => Math.max(m, x.health?.temperatureC ?? 0), 0)
  return {
    diskFault: d.disks.some((x) => ['fault', 'warn'].includes(diskState(x))),
    poolDegraded: ps.some((v) => v.status === 'degraded' || v.status === 'failed'),
    sync: d.volumes.some((v) => v.sync),
    overtemp: hottest >= 50 || [...kinds].some((k) => /hot|temp/.test(k)),
    netLink: (d.network?.ports ?? []).some((p) => p.link),
    onBattery: !!ups && /battery/.test(ups.detail ?? ''),
    onMains: !ups || !/battery/.test(ups.detail ?? ''),
    backupFail: d.backups.some((b) => b.lastRun?.status === 'failed') || kinds.has('backup_failed'),
  }
}
