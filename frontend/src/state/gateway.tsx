// App-wide gateway state: config, the live request list and connection
// status. Pages read it instead of each opening their own event stream.
import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { api, onRequest, type GatewayConfig, type RequestRecord } from '../lib/api';

const MAX = 500;

type Gateway = {
  config: GatewayConfig | null;
  configError: string | null;
  reloadConfig: () => Promise<void>;
  requests: RequestRecord[];
  requestsLoaded: boolean;
  requestsError: string | null;
  /** Connected to the live event stream. */
  live: boolean;
  /** Increments on every new request; pages refetch aggregates when it changes. */
  tick: number;
  switchRoute: (name: string) => Promise<void>;
};

const Ctx = createContext<Gateway | null>(null);

export function GatewayProvider({ children }: { children: ReactNode }) {
  const [config, setConfig] = useState<GatewayConfig | null>(null);
  const [configError, setConfigError] = useState<string | null>(null);
  const [requests, setRequests] = useState<RequestRecord[]>([]);
  const [requestsLoaded, setLoaded] = useState(false);
  const [requestsError, setRequestsError] = useState<string | null>(null);
  const [live, setLive] = useState(false);
  const [tick, setTick] = useState(0);
  // New requests are batched into one render per animation frame, so a busy
  // agent does not re-render the whole app for every event.
  const pending = useRef<RequestRecord[]>([]);
  const frame = useRef(0);

  const reloadConfig = useCallback(async () => {
    try {
      setConfig(await api.config());
      setConfigError(null);
    } catch (e) {
      setConfigError(String(e instanceof Error ? e.message : e));
    }
  }, []);

  useEffect(() => {
    reloadConfig();
    const loadRequests = () =>
      api
        .requests()
        .then((rs) => {
          setRequests(rs);
          setRequestsError(null);
        })
        .catch((e) => setRequestsError(String(e instanceof Error ? e.message : e)))
        .finally(() => setLoaded(true));
    loadRequests();
    let wasLive = false;
    const off = onRequest(
      (r) => {
        pending.current.push(r);
        if (frame.current) return;
        frame.current = requestAnimationFrame(() => {
          frame.current = 0;
          const batch = pending.current.reverse();
          pending.current = [];
          setRequests((rs) => {
            const seen = new Set(rs.map((x) => x.id));
            return [...batch.filter((x) => !seen.has(x.id)), ...rs].slice(0, MAX);
          });
          setTick((t) => t + 1);
        });
      },
      (on) => {
        setLive(on);
        // After a reconnect, catch up on what was missed while offline.
        if (on && !wasLive) loadRequests();
        wasLive = on;
      },
    );
    return () => {
      off();
      if (frame.current) cancelAnimationFrame(frame.current);
    };
  }, [reloadConfig]);

  const switchRoute = useCallback(async (name: string) => {
    await api.setRoute(name);
    setConfig((c) => (c ? { ...c, active_route: name } : c));
  }, []);

  const value = useMemo<Gateway>(
    () => ({ config, configError, reloadConfig, requests, requestsLoaded, requestsError, live, tick, switchRoute }),
    [config, configError, reloadConfig, requests, requestsLoaded, requestsError, live, tick, switchRoute],
  );
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useGateway(): Gateway {
  const v = useContext(Ctx);
  if (!v) throw new Error('useGateway outside GatewayProvider');
  return v;
}

/**
 * Fetch something and refetch it when deps change. Keeps the previous data
 * while reloading, so live refreshes never flash a spinner.
 */
export function useFetch<T>(fn: () => Promise<T>, deps: unknown[]) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [cause, setCause] = useState<unknown>(null);
  const [loading, setLoading] = useState(true);
  const seq = useRef(0);
  const run = useCallback(() => {
    const n = ++seq.current;
    setLoading(true);
    fn()
      .then((d) => {
        if (n !== seq.current) return;
        setData(d);
        setError(null);
        setCause(null);
      })
      .catch((e) => {
        if (n !== seq.current) return;
        setError(String(e instanceof Error ? e.message : e));
        setCause(e);
      })
      .finally(() => n === seq.current && setLoading(false));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);
  useEffect(run, [run]);
  return { data, error, cause, loading, reload: run, setData };
}

/** Like useFetch, but refetches at most once per `ms` as live traffic arrives. */
export function useLiveFetch<T>(fn: () => Promise<T>, deps: unknown[], ms = 2000) {
  const { tick } = useGateway();
  const [slowTick, setSlowTick] = useState(0);
  const last = useRef(0);
  useEffect(() => {
    if (tick === 0) return;
    const wait = Math.max(0, last.current + ms - Date.now());
    const id = window.setTimeout(() => {
      last.current = Date.now();
      setSlowTick((x) => x + 1);
    }, wait);
    return () => window.clearTimeout(id);
  }, [tick, ms]);
  return useFetch(fn, [...deps, slowTick]);
}
