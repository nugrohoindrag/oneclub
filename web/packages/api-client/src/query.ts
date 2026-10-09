import { QueryClient, useMutation, useQuery, useQueryClient, type UseQueryOptions } from '@tanstack/react-query';
import { ApiError, getActiveProperty, request } from './index';

/** Shared QueryClient defaults: no retry on 4xx, short stale time. */
export function createQueryClient() {
  return new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 15_000,
        refetchOnWindowFocus: false,
        retry: (count, err) => !(err instanceof ApiError && err.status < 500) && count < 2,
      },
      mutations: { retry: false },
    },
  });
}

export interface Page<T> {
  items: T[];
  nextCursor?: string;
  /** Server paging (?limit=): the number of rows, known on the last page. */
  total?: number;
}

/** GET a path; the active property is part of the cache key. */
export function useGet<T>(path: string | null, options?: Omit<UseQueryOptions<T, ApiError>, 'queryKey' | 'queryFn'>) {
  return useQuery<T, ApiError>({
    queryKey: [path, getActiveProperty()],
    queryFn: () => request<T>('GET', path as string),
    enabled: path !== null && (options?.enabled ?? true),
    ...options,
  });
}

/**
 * Resolves a mutation's URL and body. When the path is a function, the keys it
 * reads (the ids that only build the URL, e.g. `playerId`) are left out of the
 * body: the API decodes strictly and rejects unknown fields.
 */
export function resolveSend<TBody>(path: string | ((vars: TBody) => string), vars: TBody): { url: string; body: TBody } {
  if (typeof path !== 'function') return { url: path, body: vars };
  if (vars === null || typeof vars !== 'object' || Array.isArray(vars)) return { url: path(vars), body: vars };
  const read = new Set<PropertyKey>();
  const spy = new Proxy(vars as object, { get: (t, k, r) => { read.add(k); return Reflect.get(t, k, r); } }) as TBody;
  const url = path(spy);
  const body = Object.fromEntries(Object.entries(vars as object).filter(([k]) => !read.has(k))) as TBody;
  return { url, body };
}

/** Mutation helper that invalidates the given path prefixes on success. */
export function useSend<TBody = unknown, TRes = unknown>(
  method: 'POST' | 'PATCH' | 'PUT' | 'DELETE',
  path: string | ((vars: TBody) => string),
  invalidate: string[] = [],
  headers?: () => Record<string, string>,
) {
  const qc = useQueryClient();
  return useMutation<TRes, ApiError, TBody>({
    mutationFn: (vars) => {
      const { url, body } = resolveSend(path, vars);
      return request<TRes>(method, url, method === 'DELETE' ? undefined : body, headers?.());
    },
    onSuccess: () => {
      for (const p of invalidate) {
        qc.invalidateQueries({ predicate: (q) => typeof q.queryKey[0] === 'string' && (q.queryKey[0] as string).startsWith(p) });
      }
    },
  });
}

/** Builds a query string from defined values. */
export function qs(params: Record<string, string | number | boolean | undefined | null>) {
  const s = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== null && v !== '') s.set(k, String(v));
  const str = s.toString();
  return str ? `?${str}` : '';
}
