/**
 * OneClub API client — generated from the published OpenAPI document
 * (Technical Doc §6.3: no hand-written fetch to endpoints). Types come from
 * `schema.ts` (regenerated with `pnpm generate`, checked for drift in CI).
 */
import createClient, { type Middleware } from 'openapi-fetch';
import type { paths, components } from './schema';

export type { paths, components };
export type Schemas = components['schemas'];

/** RFC 9457 Problem Details as returned by the API. */
export interface Problem {
  type: string;
  title: string;
  status: number;
  detail?: string;
  code?: string;
  requestId?: string;
  errors?: { field: string; code: string; message: string }[];
}

/** Error thrown for non-2xx responses. */
export class ApiError extends Error {
  readonly status: number;
  readonly problem: Problem;
  constructor(problem: Problem) {
    super(problem.detail || problem.title);
    this.status = problem.status;
    this.problem = problem;
  }
  /** Field errors keyed by field name. */
  get fieldErrors(): Record<string, string> {
    const out: Record<string, string> = {};
    for (const e of this.problem.errors ?? []) out[e.field] = e.message;
    return out;
  }
  get code(): string | undefined {
    return this.problem.code;
  }
}

// ── request context (active property, locale) ──────────────────────────────

type Listener = () => void;
const state = { propertyId: '' as string, locale: '' as string };
const listeners = new Set<Listener>();
const PROPERTY_KEY = 'oneclub.activeProperty';

try {
  state.propertyId = localStorage.getItem(PROPERTY_KEY) ?? '';
} catch {
  /* storage unavailable */
}

/** Sets the active property chosen in the property switcher (FR-ORG-06). */
export function setActiveProperty(id: string) {
  state.propertyId = id;
  try {
    localStorage.setItem(PROPERTY_KEY, id);
  } catch {
    /* ignore */
  }
  listeners.forEach((l) => l());
}

export function getActiveProperty() {
  return state.propertyId;
}

export function onActivePropertyChange(l: Listener) {
  listeners.add(l);
  return () => {
    listeners.delete(l);
  };
}

export function setRequestLocale(locale: string) {
  state.locale = locale;
}

/** Base URL; empty = same origin (Caddy routes /api to the API). */
export const API_BASE: string = (import.meta as { env?: Record<string, string> }).env?.VITE_API_BASE ?? '';

const contextMiddleware: Middleware = {
  onRequest({ request }) {
    if (state.propertyId && !request.headers.has('X-Property-Id')) request.headers.set('X-Property-Id', state.propertyId);
    if (state.locale) request.headers.set('X-Locale', state.locale);
    return request;
  },
};

let unauthorizedHandler: ((p: Problem) => void) | undefined;
/** Called on 401/403 problem codes (session expired, MFA, suspended …). */
export function onAuthProblem(h: (p: Problem) => void) {
  unauthorizedHandler = h;
}

const errorMiddleware: Middleware = {
  async onResponse({ response }) {
    if (response.ok) return response;
    let problem: Problem;
    try {
      problem = (await response.clone().json()) as Problem;
    } catch {
      problem = { type: 'about:blank', title: response.statusText, status: response.status };
    }
    if (!problem.status) problem.status = response.status;
    if (
      unauthorizedHandler &&
      (response.status === 401 ||
        problem.code === 'mfa_required' ||
        problem.code === 'password_change_required' ||
        response.status === 503)
    ) {
      unauthorizedHandler(problem);
    }
    throw new ApiError(problem);
  },
};

/** Typed client: `api.GET('/api/v1/platform/venues', { params: { query: { q } } })`. */
export const api = createClient<paths>({ baseUrl: API_BASE, credentials: 'include' });
api.use(contextMiddleware);
api.use(errorMiddleware);

/** Unwraps `{ data }` from openapi-fetch (errors are thrown by middleware). */
export async function unwrap<T>(p: Promise<{ data?: T; error?: unknown; response: Response }>): Promise<T> {
  const r = await p;
  return r.data as T;
}

/**
 * Raw request for endpoints whose path is computed at runtime (generic master
 * data pages). Still goes through the same middlewares and error handling.
 */
export async function request<T = unknown>(
  method: 'GET' | 'POST' | 'PATCH' | 'PUT' | 'DELETE',
  path: string,
  body?: unknown,
  headers?: Record<string, string>,
): Promise<T> {
  const h = new Headers(headers);
  if (state.propertyId && !h.has('X-Property-Id')) h.set('X-Property-Id', state.propertyId);
  if (state.locale) h.set('X-Locale', state.locale);
  let payload: BodyInit | undefined;
  if (body instanceof FormData) payload = body;
  else if (body !== undefined) {
    h.set('Content-Type', 'application/json');
    payload = JSON.stringify(body);
  }
  const res = await fetch(API_BASE + path, { method, headers: h, body: payload, credentials: 'include' });
  if (!res.ok) {
    await errorMiddleware.onResponse!({ response: res } as never);
  }
  if (res.status === 204) return undefined as T;
  const ct = res.headers.get('Content-Type') ?? '';
  if (ct.includes('json')) return (await res.json()) as T;
  return (await res.blob()) as T;
}

/** Downloads a file response (exports) and saves it in the browser. */
export async function download(method: 'GET' | 'POST', path: string, body?: unknown, filename?: string) {
  const blob = await request<Blob>(method, path, body);
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename ?? 'export';
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

/** UUIDv7 (time ordered) for client-generated ids and idempotency keys. */
export function uuidv7(): string {
  const bytes = new Uint8Array(16);
  crypto.getRandomValues(bytes);
  const ts = Date.now();
  bytes[0] = (ts / 2 ** 40) & 0xff;
  bytes[1] = (ts / 2 ** 32) & 0xff;
  bytes[2] = (ts / 2 ** 24) & 0xff;
  bytes[3] = (ts / 2 ** 16) & 0xff;
  bytes[4] = (ts / 2 ** 8) & 0xff;
  bytes[5] = ts & 0xff;
  bytes[6] = (bytes[6] & 0x0f) | 0x70;
  bytes[8] = (bytes[8] & 0x3f) | 0x80;
  const h = [...bytes].map((b) => b.toString(16).padStart(2, '0')).join('');
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`;
}

export * from './query';
