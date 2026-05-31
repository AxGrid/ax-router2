import { useEffect, useRef, useState } from 'react';
import { State } from './types';
import { formatBytes, formatDurationShort } from './format';
import ServiceCard from './ServiceCard';

type ConnState = 'connecting' | 'open' | 'closed';

export default function App() {
  const [state, setState] = useState<State | null>(null);
  const [conn, setConn] = useState<ConnState>('connecting');
  const wsRef = useRef<WebSocket | null>(null);

  useEffect(() => {
    let alive = true;
    let attempt = 0;

    const connect = () => {
      if (!alive) return;
      attempt++;
      setConn('connecting');
      const proto = location.protocol === 'https:' ? 'wss' : 'ws';
      const ws = new WebSocket(`${proto}://${location.host}/__router/ws`);
      wsRef.current = ws;
      ws.onopen = () => {
        attempt = 0;
        setConn('open');
      };
      ws.onmessage = (ev) => {
        try {
          const data: State = JSON.parse(ev.data);
          setState(data);
        } catch (e) {
          console.error(e);
        }
      };
      ws.onclose = () => {
        setConn('closed');
        if (!alive) return;
        const delay = Math.min(8000, 250 * 2 ** Math.min(attempt, 5));
        setTimeout(connect, delay);
      };
      ws.onerror = () => {
        ws.close();
      };
    };
    connect();

    // Initial fetch in case WS takes a beat to send.
    fetch('/__router/api/state')
      .then((r) => r.json())
      .then((d: State) => {
        if (alive && !state) setState(d);
      })
      .catch(() => {});

    return () => {
      alive = false;
      wsRef.current?.close();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  if (!state) {
    return (
      <div className="grid min-h-screen place-items-center text-brand-700">
        <div className="font-mono text-sm tracking-wider opacity-70">loading…</div>
      </div>
    );
  }

  const totals = state.services.reduce(
    (acc, s) => {
      acc.connected += s.connected ? 1 : 0;
      acc.httpActive += s.httpActive;
      acc.wsActive += s.wsActive;
      acc.bytesIn += s.httpBytesIn + s.wsBytesIn;
      acc.bytesOut += s.httpBytesOut + s.wsBytesOut;
      return acc;
    },
    { connected: 0, httpActive: 0, wsActive: 0, bytesIn: 0, bytesOut: 0 }
  );

  return (
    <div className="mx-auto max-w-7xl px-6 py-10">
      <header className="mb-10 flex flex-wrap items-end justify-between gap-6 border-b pb-6">
        <div>
          <div className="flex items-center gap-3">
            <Logo />
            <h1 className="font-mono text-2xl font-bold tracking-tight text-brand-800">
              ax-router
            </h1>
            <span className="pill-off">{state.baseDomain}</span>
          </div>
          <p className="mt-2 font-mono text-xs text-fg-muted">
            uptime {formatDurationShort(state.nowMs - state.startedAtMs)} ·{' '}
            {state.tokenCount} token{state.tokenCount === 1 ? '' : 's'}
            {state.tokensReloadedAtMs > 0 && (
              <>
                {' '}· reloaded {formatDurationShort(state.nowMs - state.tokensReloadedAtMs)} ago
              </>
            )}
            {state.tokensError && (
              <span className="ml-2 text-danger">
                · token error: {state.tokensError}
              </span>
            )}
          </p>
        </div>

        <div className="flex items-center gap-2 font-mono text-[11px] uppercase tracking-widest">
          <span
            className={`h-2 w-2 rounded-full ${
              conn === 'open' ? 'led-on' : conn === 'connecting' ? 'bg-brand-600' : 'led-off'
            }`}
          />
          <span className="text-fg-muted">live · {conn}</span>
        </div>
      </header>

      <section className="mb-10 grid grid-cols-2 gap-4 md:grid-cols-5">
        <Tile label="Services" value={String(state.services.length)} sub={`${totals.connected} connected`} />
        <Tile label="HTTP active" value={String(totals.httpActive)} />
        <Tile label="WS active" value={String(totals.wsActive)} />
        <Tile label="Bytes in" value={formatBytes(totals.bytesIn)} />
        <Tile label="Bytes out" value={formatBytes(totals.bytesOut)} />
      </section>

      {state.services.length === 0 ? (
        <div className="card grid place-items-center py-16 text-center">
          <p className="font-mono text-brand-700">No services have ever connected.</p>
          <p className="mt-2 max-w-md text-sm text-fg-muted">
            Connect a router-client with a configured token; this dashboard updates in
            real time.
          </p>
        </div>
      ) : (
        <div className="grid gap-5 sm:grid-cols-2 xl:grid-cols-3">
          {state.services.map((s) => (
            <ServiceCard
              key={s.service}
              svc={s}
              now={state.nowMs}
              cert={state.certs?.[`${s.service}.${state.baseDomain}`]}
            />
          ))}
        </div>
      )}

      <TokensPanel />

      <footer className="mt-12 text-center font-mono text-[11px] tracking-wider text-fg-muted">
        ax-router2 · github.com/axgrid/ax-router2
      </footer>
    </div>
  );
}

type TokenInfo = { service: string; token: string; wildcard: boolean };

function maskToken(t: string): string {
  if (!t) return '';
  if (t.length <= 4) return '•'.repeat(t.length);
  return '•'.repeat(Math.min(t.length - 4, 8)) + t.slice(-4);
}

// TokensPanel lazily fetches /__router/api/tokens (admin-auth only) and shows
// each token masked, with per-row reveal and copy-to-clipboard of the full
// value. If admin auth isn't configured the endpoint returns 403 and we show a
// hint instead of any tokens.
function TokensPanel() {
  const [open, setOpen] = useState(false);
  const [tokens, setTokens] = useState<TokenInfo[] | null>(null);
  const [err, setErr] = useState('');
  const [revealed, setRevealed] = useState<Record<string, boolean>>({});
  const [copied, setCopied] = useState('');

  const load = async () => {
    setErr('');
    setTokens(null);
    try {
      const r = await fetch('/__router/api/tokens', { cache: 'no-store' });
      if (r.status === 403) {
        setErr('Token viewer requires admin auth (AXR_ADMIN_USER / AXR_ADMIN_PASS).');
        setTokens([]);
        return;
      }
      if (!r.ok) {
        setErr(`Failed to load tokens (${r.status}).`);
        setTokens([]);
        return;
      }
      setTokens((await r.json()) as TokenInfo[]);
    } catch {
      setErr('Failed to load tokens.');
      setTokens([]);
    }
  };

  const toggle = () => {
    const next = !open;
    setOpen(next);
    if (next && tokens === null) load();
  };

  const copy = async (tok: string) => {
    try {
      await navigator.clipboard.writeText(tok);
      setCopied(tok);
      setTimeout(() => setCopied((c) => (c === tok ? '' : c)), 1500);
    } catch {
      /* clipboard unavailable (insecure context) */
    }
  };

  return (
    <section className="mt-10">
      <div className="mb-3 flex items-center justify-between">
        <h2 className="stat-label">Tokens</h2>
        <button
          onClick={toggle}
          className="rounded-lg border bg-surface px-3 py-1.5 font-mono text-[11px] uppercase tracking-widest text-fg-muted transition hover:text-brand-700"
        >
          {open ? 'Hide tokens' : 'Show tokens'}
        </button>
      </div>

      {open && (
        <div className="card overflow-hidden p-0">
          {err ? (
            <p className="p-4 text-sm text-fg-muted">{err}</p>
          ) : tokens === null ? (
            <p className="p-4 font-mono text-sm text-fg-muted">loading…</p>
          ) : tokens.length === 0 ? (
            <p className="p-4 text-sm text-fg-muted">No tokens configured.</p>
          ) : (
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b text-left">
                  <th className="stat-label px-4 py-2.5 font-normal">Service</th>
                  <th className="stat-label px-4 py-2.5 font-normal">Token</th>
                  <th className="stat-label px-4 py-2.5 text-right font-normal">Actions</th>
                </tr>
              </thead>
              <tbody>
                {tokens.map((t) => (
                  <tr key={t.token} className="border-b last:border-0">
                    <td className="px-4 py-2.5 font-mono text-brand-800">
                      {t.wildcard ? 'any (*)' : t.service}
                    </td>
                    <td className="px-4 py-2.5">
                      <code className="rounded bg-surface-2 px-2 py-1 font-mono text-xs text-brand-700">
                        {revealed[t.token] ? t.token : maskToken(t.token)}
                      </code>
                    </td>
                    <td className="px-4 py-2.5">
                      <div className="flex items-center justify-end gap-2">
                        <button
                          onClick={() => setRevealed((m) => ({ ...m, [t.token]: !m[t.token] }))}
                          className="rounded-md border px-2 py-1 font-mono text-[11px] uppercase tracking-wider text-fg-muted transition hover:text-fg"
                        >
                          {revealed[t.token] ? 'Hide' : 'Reveal'}
                        </button>
                        <button
                          onClick={() => copy(t.token)}
                          className="rounded-md border px-2 py-1 font-mono text-[11px] uppercase tracking-wider text-fg-muted transition hover:text-brand-700"
                        >
                          {copied === t.token ? 'Copied!' : 'Copy'}
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      )}
    </section>
  );
}

function Tile({ label, value, sub }: { label: string; value: string; sub?: string }) {
  return (
    <div className="rounded-lg border bg-surface p-4">
      <div className="stat-label">{label}</div>
      <div className="mt-1 font-mono text-2xl text-brand-800">{value}</div>
      {sub && <div className="mt-0.5 stat-sub">{sub}</div>}
    </div>
  );
}

function Logo() {
  return (
    <svg
      width="28"
      height="28"
      viewBox="0 0 32 32"
      style={{ filter: 'drop-shadow(0 0 10px color-mix(in oklab, var(--accent) 45%, transparent))' }}
    >
      <defs>
        <linearGradient id="lg" x1="0" x2="1" y1="0" y2="1">
          <stop offset="0%" stopColor="var(--brand-500)" />
          <stop offset="100%" stopColor="var(--brand-700)" />
        </linearGradient>
      </defs>
      <rect x="2" y="2" width="28" height="28" rx="7" fill="var(--bg)" stroke="url(#lg)" strokeWidth="1.5" />
      <path
        d="M8 22 L14 10 L20 22 M10 18 L18 18"
        fill="none"
        stroke="url(#lg)"
        strokeWidth="2"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
      <circle cx="24" cy="22" r="2" fill="var(--brand-500)" />
    </svg>
  );
}
