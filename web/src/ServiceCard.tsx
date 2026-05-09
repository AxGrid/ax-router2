import { CertState, Service } from './types';
import { formatBytes, formatLatency, formatNumber, formatDurationShort } from './format';
import Sparkline from './Sparkline';

interface Props {
  svc: Service;
  now: number;
  cert?: CertState;
}

export default function ServiceCard({ svc, now, cert }: Props) {
  const totalRps = svc.rps60.reduce((a, b) => a + b, 0);
  const totalBps = svc.bps60.reduce((a, b) => a + b, 0);

  const since = svc.connected
    ? svc.connectedAtMs
      ? formatDurationShort(now - svc.connectedAtMs)
      : '—'
    : svc.disconnectedAtMs
      ? `${formatDurationShort(now - svc.disconnectedAtMs)} ago`
      : '—';

  return (
    <div className="card">
      <div className="flex items-start justify-between gap-4">
        <div>
          <div className="flex items-center gap-3">
            <span
              className={`inline-block h-2.5 w-2.5 rounded-full ${
                svc.connected ? 'led-on' : 'led-off'
              }`}
              aria-hidden
            />
            <h2 className="font-mono text-xl font-semibold tracking-tight text-brand-800">
              {svc.service}
            </h2>
          </div>
          <p className="mt-1 font-mono text-xs text-fg-muted">
            {svc.connected
              ? svc.remote ?? 'connected'
              : 'offline'}
          </p>
        </div>
        <div className="flex flex-col items-end gap-1.5">
          <span className={svc.connected ? 'pill-on' : 'pill-off'}>
            {svc.connected ? 'connected' : 'offline'}
          </span>
          {cert && cert.status && <CertPill cert={cert} />}
        </div>
      </div>

      <div className="mt-4 grid grid-cols-2 gap-4 text-sm">
        <div>
          <div className="stat-label">{svc.connected ? 'Connected for' : 'Down for'}</div>
          <div className="stat-value">{since}</div>
        </div>
        <div>
          <div className="stat-label">Active streams</div>
          <div className="stat-value">
            {svc.httpActive}
            <span className="stat-sub ml-1">+ {svc.wsActive} ws</span>
          </div>
        </div>
      </div>

      <div className="mt-4 grid grid-cols-3 gap-3 text-sm">
        <Stat label="HTTP req" value={formatNumber(svc.httpRequests)} />
        <Stat label="In" value={formatBytes(svc.httpBytesIn)} />
        <Stat label="Out" value={formatBytes(svc.httpBytesOut)} />
        <Stat label="WS up" value={formatNumber(svc.wsUpgrades)} />
        <Stat label="WS in" value={formatBytes(svc.wsBytesIn)} />
        <Stat label="WS out" value={formatBytes(svc.wsBytesOut)} />
      </div>

      <div className="mt-4 grid grid-cols-3 gap-3 text-sm">
        <Stat label="p50 latency" value={formatLatency(svc.latencyP50Us)} />
        <Stat label="p95 latency" value={formatLatency(svc.latencyP95Us)} />
        <Stat label="p99 latency" value={formatLatency(svc.latencyP99Us)} />
      </div>

      <div className="mt-5">
        <div className="flex items-baseline justify-between">
          <span className="stat-label">RPS · last 60s</span>
          <span className="stat-sub">
            {totalRps} req · {formatBytes(totalBps)}
          </span>
        </div>
        <div className="mt-1.5 rounded-md bg-surface-2 p-2">
          <Sparkline values={svc.rps60} />
        </div>
      </div>
    </div>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <div className="stat-label">{label}</div>
      <div className="font-mono text-sm text-brand-900">{value}</div>
    </div>
  );
}

function CertPill({ cert }: { cert: CertState }) {
  if (cert.status === 'pending') {
    const elapsed = cert.elapsedMs ? Math.floor(cert.elapsedMs / 1000) : 0;
    return (
      <span className="pill-on">
        <span className="led-on inline-block h-1.5 w-1.5 rounded-full" />
        cert · issuing {elapsed}s
      </span>
    );
  }
  if (cert.status === 'ready') {
    return (
      <span className="pill-on">
        <span className="inline-block h-1.5 w-1.5 rounded-full bg-brand-700" />
        cert · ready
      </span>
    );
  }
  if (cert.status === 'error') {
    return (
      <span className="pill-danger" title={cert.error}>
        <span
          className="inline-block h-1.5 w-1.5 rounded-full"
          style={{ background: 'var(--danger)' }}
        />
        cert · error
      </span>
    );
  }
  return null;
}
