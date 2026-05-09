export interface Service {
  service: string;
  connected: boolean;
  remote?: string;
  connectedAtMs?: number;
  disconnectedAtMs?: number;
  httpRequests: number;
  httpBytesIn: number;
  httpBytesOut: number;
  httpActive: number;
  wsUpgrades: number;
  wsBytesIn: number;
  wsBytesOut: number;
  wsActive: number;
  latencyP50Us: number;
  latencyP95Us: number;
  latencyP99Us: number;
  rps60: number[];
  bps60: number[];
}

export type IssuanceStatus = '' | 'pending' | 'ready' | 'error';

export interface CertState {
  host: string;
  status: IssuanceStatus;
  startedAtMs?: number;
  doneAtMs?: number;
  elapsedMs?: number;
  error?: string;
}

export interface State {
  baseDomain: string;
  startedAtMs: number;
  nowMs: number;
  services: Service[];
  tlsMode: string;
  certs?: Record<string, CertState>;
  tokenCount: number;
  tokensReloadedAtMs: number;
  tokensError?: string;
}
