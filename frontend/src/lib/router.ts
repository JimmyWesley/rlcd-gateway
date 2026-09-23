// A small hash router: #/section/sub/param?query. Hash routing keeps deep
// links working on the embedded /ui/ server and on the Vite dev server alike.
import { useCallback, useEffect, useState } from 'react';

export type Location = { path: string[]; query: URLSearchParams };

function parse(): Location {
  const raw = window.location.hash.replace(/^#\/?/, '');
  const [p, q = ''] = raw.split('?');
  return { path: p.split('/').filter(Boolean).map(decodeURIComponent), query: new URLSearchParams(q) };
}

export function href(path: string, query?: Record<string, string | undefined | null>): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(query ?? {})) if (v) q.set(k, v);
  const s = q.toString();
  return `#/${path.replace(/^\//, '')}${s ? `?${s}` : ''}`;
}

export function navigate(path: string, query?: Record<string, string | undefined | null>, replace = false) {
  const h = href(path, query);
  if (replace) window.history.replaceState(null, '', h);
  else window.history.pushState(null, '', h);
  window.dispatchEvent(new HashChangeEvent('hashchange'));
}

export function useLocation(): Location {
  const [loc, setLoc] = useState(parse);
  useEffect(() => {
    const on = () => setLoc(parse());
    window.addEventListener('hashchange', on);
    window.addEventListener('popstate', on);
    return () => {
      window.removeEventListener('hashchange', on);
      window.removeEventListener('popstate', on);
    };
  }, []);
  return loc;
}

/** Read and write one query parameter of the current location. */
export function useQueryParam(name: string): [string, (v: string | null) => void] {
  const loc = useLocation();
  const set = useCallback(
    (v: string | null) => {
      const cur = parse();
      const q: Record<string, string> = {};
      cur.query.forEach((val, k) => (q[k] = val));
      if (v) q[name] = v;
      else delete q[name];
      navigate(cur.path.join('/'), q, true);
    },
    [name],
  );
  return [loc.query.get(name) ?? '', set];
}
