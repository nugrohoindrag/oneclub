import { Link } from 'react-router';
import { formatDateTime } from '@oneclub/i18n';
import { clearAll, flush, setForcedOffline, useOnline, useQueue } from '@oneclub/offline';
import { DataTable, Icon, StatusPill, areaPath, useArea } from '@oneclub/shell';

/*
 * Shared by the offline areas (Operational, Caddy Tablet; Technical Doc §6.4):
 * device registration, connectivity chip and the sync queue.
 */

export const DEVICE_KEY = 'oneclub.deviceToken';
export const OUTLET_KEY = 'oneclub.outlet';

export function read(k: string) {
  try {
    return localStorage.getItem(k) ?? '';
  } catch {
    return '';
  }
}

export function write(k: string, v: string) {
  try {
    if (v) localStorage.setItem(k, v);
    else localStorage.removeItem(k);
  } catch {
    /* ignore */
  }
}

/** Logout clears the offline data of the session (cache is per property and shift). */
export async function clearOfflineData() {
  await clearAll();
  write(OUTLET_KEY, '');
  if ('caches' in window) await caches.delete('staff-api');
}

function usePending() {
  return useQueue().filter((q) => ['queued', 'sending', 'failed'].includes(q.status)).length;
}

/** Online/offline state and pending actions; opens the sync queue of the area. */
export function ConnectivityChip() {
  const online = useOnline();
  const pending = usePending();
  const area = useArea();
  return (
    <Link to={area ? areaPath(area, 'sync') : '/'} className="oc-chip" aria-label={`${online ? 'Online' : 'Offline'}, ${pending} pending`}>
      <Icon name={online ? 'cloud_done' : 'cloud_off'} size={18} /> {online ? 'Online' : 'Offline'}{pending > 0 && ` · ${pending}`}
    </Link>
  );
}

/** Sync queue with simulated offline mode (FR-SH-05 acceptance). */
export function SyncPage() {
  const online = useOnline();
  const items = useQueue();
  return (
    <div className="oc-stack">
      <div className="oc-page-head">
        <div><h1>Sync Queue</h1><p>Actions recorded on this device, sent in order when online.</p></div>
        <span className="oc-spacer" />
        <button className="oc-btn oc-btn-outline" onClick={() => setForcedOffline(online)}>{online ? 'Simulate offline' : 'Go back online'}</button>
        <button className="oc-btn oc-btn-ink" disabled={!online} onClick={() => void flush()}>Sync now</button>
      </div>
      <div className="oc-card">
        <DataTable rows={items as unknown as Record<string, unknown>[]}
          columns={[
            { key: 'createdAt', header: 'Recorded', render: (i) => formatDateTime(String(i.createdAt)) },
            { key: 'action', header: 'Action', render: (i) => <code className="oc-code">{String((i.payload as Record<string, unknown>)?.op ?? i.action)}</code> },
            { key: 'payload', header: 'Data', render: (i) => <span className="oc-small">{JSON.stringify(i.payload).slice(0, 80)}</span> },
            { key: 'attempts', header: 'Attempts', align: 'right' },
            { key: 'status', header: 'Status', render: (i) => (
              <div><StatusPill status={String(i.status)} />{i.error ? <div className="oc-small oc-muted">{String(i.error)}</div> : null}</div>
            ) },
          ]} />
      </div>
    </div>
  );
}
