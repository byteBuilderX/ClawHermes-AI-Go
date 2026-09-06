import axios, {
  type AxiosInstance,
  type AxiosRequestConfig,
  type InternalAxiosRequestConfig,
} from 'axios';

import { API_DEFAULT_TIMEOUT_MS } from '@/constants';

interface RetryableConfig extends InternalAxiosRequestConfig {
  _retry?: boolean;
}

type TokenRef = { current: string | null };
type LogoutHandler = () => void;
type StreamEventHandler = (event: unknown) => boolean | void;
export interface ServerSentEvent { id?: string; event?: string; data: unknown }

export class StreamRequestError extends Error {
  constructor(
    message: string,
    public readonly status?: number,
    public readonly code?: string,
  ) {
    super(message);
    this.name = 'StreamRequestError';
  }
}

const api: AxiosInstance = axios.create({
  baseURL: import.meta.env.VITE_API_BASE_URL || '',
  timeout: API_DEFAULT_TIMEOUT_MS,
  withCredentials: true,
});

let _tokenRef: TokenRef = { current: null };
let _reqInterceptor: number | null = null;
let _resInterceptor: number | null = null;

let _authReady = false;
let _authReadyResolve: (() => void) | null = null;
let _authReadyPromise: Promise<void> = new Promise<void>((resolve) => {
  _authReadyResolve = resolve;
});

const AUTH_READY_TIMEOUT_MS = 8000;

const PUBLIC_PATH_PREFIXES = [
  '/auth/refresh',
  '/auth/logout',
  '/auth/github',
  '/auth/guest',
  '/auth/register',
  '/auth/callback',
  '/health',
  '/metrics',
];

const isPublicRequest = (url: string | undefined): boolean => {
  if (!url) return false;
  return PUBLIC_PATH_PREFIXES.some((p) => url === p || url.startsWith(`${p}?`) || url.startsWith(`${p}/`));
};

const waitWithTimeout = <T,>(p: Promise<T>, ms: number): Promise<T> =>
  new Promise<T>((resolve, reject) => {
    const t = setTimeout(() => reject(new Error('auth ready timeout')), ms);
    p.then((v) => { clearTimeout(t); resolve(v); }, (e) => { clearTimeout(t); reject(e); });
  });

export const markAuthReady = (): void => {
  if (_authReady) return;
  _authReady = true;
  _authReadyResolve?.();
};

export const resetAuthReady = (): void => {
  _authReady = false;
  _authReadyPromise = new Promise<void>((resolve) => {
    _authReadyResolve = resolve;
  });
};

export const getTokenRef = (): TokenRef => _tokenRef;

const setAuthHeader = (config: AxiosRequestConfig, token: string | null) => {
  const headers = config.headers as Record<string, unknown> | undefined;
  if (!headers) return;
  if (token === null) {
    if (typeof (headers as { delete?: (k: string) => void }).delete === 'function') {
      (headers as { delete: (k: string) => void }).delete('Authorization');
    } else {
      delete (headers as Record<string, unknown>).Authorization;
    }
    return;
  }
  if (typeof (headers as { set?: (k: string, v: string) => void }).set === 'function') {
    (headers as { set: (k: string, v: string) => void }).set('Authorization', `Bearer ${token}`);
  } else {
    (headers as Record<string, unknown>).Authorization = `Bearer ${token}`;
  }
};

let _logoutHandler: LogoutHandler | null = null;
let _refreshInFlight: Promise<string | null> | null = null;

// refreshAccessToken 是 axios 与 SSE 共享的单飞刷新：并发 401（普通请求与流
// 请求同帧过期）只触发一次 /auth/refresh，其余调用方复用同一 in-flight promise，
// 避免 refresh token 轮换竞争（RT 单次消费）。成功写回 _tokenRef；失败触发登出
// 回调并返回 null（会话确已失效，调用方向上抛原 401）。
const refreshAccessToken = (): Promise<string | null> => {
  if (_refreshInFlight) return _refreshInFlight;
  _refreshInFlight = api
    .post<{ access_token: string }>('/auth/refresh')
    .then((res) => {
      const token = res.data.access_token;
      _tokenRef.current = token;
      return token;
    })
    .catch(() => {
      _logoutHandler?.();
      return null;
    })
    .finally(() => {
      _refreshInFlight = null;
    });
  return _refreshInFlight;
};

export const setupApiInterceptors = (tokenRef: TokenRef, onLogout?: LogoutHandler): void => {
  _tokenRef = tokenRef;
  _logoutHandler = onLogout ?? null;

  if (_reqInterceptor !== null) api.interceptors.request.eject(_reqInterceptor);
  if (_resInterceptor !== null) api.interceptors.response.eject(_resInterceptor);

  _reqInterceptor = api.interceptors.request.use(
    async (config) => {
      const url = config.url || '';
      if (!_authReady && !isPublicRequest(url)) {
        try {
          await waitWithTimeout(_authReadyPromise, AUTH_READY_TIMEOUT_MS);
        } catch {
          // fall through; downstream will handle 401 via response interceptor
        }
      }

      const headers = config.headers as Record<string, unknown> | undefined;
      const existing =
        headers && typeof (headers as { get?: (k: string) => unknown }).get === 'function'
          ? (headers as { get: (k: string) => unknown }).get('Authorization')
          : (headers as Record<string, unknown> | undefined)?.Authorization;
      if (!existing && _tokenRef.current) {
        setAuthHeader(config, _tokenRef.current);
      }
      return config;
    },
    (error) => Promise.reject(error),
  );

  _resInterceptor = api.interceptors.response.use(
    (response) => response,
    async (error) => {
      const originalRequest = error.config as RetryableConfig | undefined;

      // 403 提示统一交给页面 catch 呈现（M2）：页面可能刻意静默 403
      // （如权限变更场景），拦截器全局弹窗会破坏静默并造成双弹。

      if (
        originalRequest &&
        error.response?.status === 401 &&
        !originalRequest._retry &&
        !originalRequest.url?.includes('/auth/refresh')
      ) {
        // 共享单飞刷新（与 SSE 流同一把锁）：并发 401 只触发一次 /auth/refresh，
        // 刷新失败会话确已失效，登出回调由 refreshAccessToken 触发，此处原样上抛。
        originalRequest._retry = true;
        const newToken = await refreshAccessToken();
        if (newToken === null) {
          return Promise.reject(error);
        }
        setAuthHeader(originalRequest, null);
        return api(originalRequest);
      }

      return Promise.reject(error);
    },
  );
};

const parseStreamError = async (response: Response): Promise<Error> => {
  try {
    const data = (await response.json()) as { error?: string; message?: string; code?: string };
    return new StreamRequestError(
      data.message || data.error || `HTTP ${response.status}`,
      response.status,
      data.code,
    );
  } catch {
    return new StreamRequestError(`HTTP ${response.status}`, response.status);
  }
};

const consumeSSE = async (
  response: Response,
  ctrl: AbortController,
  onEvent: (event: ServerSentEvent) => boolean | void,
  onClose?: () => void,
) => {
  const reader = response.body?.getReader();
  if (!reader) throw new Error('No readable stream');
  const decoder = new TextDecoder();
  let buffer = '';
  for (;;) {
    const { done, value } = await reader.read();
    if (done) { onClose?.(); return; }
    buffer += decoder.decode(value, { stream: true });
    const parts = buffer.split('\n\n');
    buffer = parts.pop() ?? '';
    for (const part of parts) {
      if (part.startsWith(':')) continue;
      const lines = part.split('\n');
      const data = lines.filter((line) => line.startsWith('data:')).map((line) => line.slice(5).trimStart()).join('\n');
      if (!data) continue;
      let parsed: unknown;
      try { parsed = JSON.parse(data); } catch { continue; }
      const envelope: ServerSentEvent = {
        id: lines.find((line) => line.startsWith('id:'))?.slice(3).trim(),
        event: lines.find((line) => line.startsWith('event:'))?.slice(6).trim(),
        data: parsed,
      };
      if (onEvent(envelope) === false) { ctrl.abort(); return; }
    }
  }
};

// fetchStreamWithAuth 对 SSE 流请求发起 fetch，并在首个响应为 401 时触发一次
// 共享单飞刷新后用新 token 重放。SSE 是长耗时请求，最容易跨过 access token
// 生命周期；refresh 失败（会话确已失效）时返回原 401 响应，由调用方 onError
// 处理——agent/workflow 消费方对 4xx 终止流、对 5xx/网络断线才重连，401 自愈
// 只发生在 refresh cookie 仍有效时。
type StreamFetchInit = (token: string | null) => RequestInit;

const fetchStreamWithAuth = async (
  path: string,
  init: StreamFetchInit,
  ctrl: AbortController,
): Promise<Response> => {
  if (!_authReady && !isPublicRequest(path)) {
    try {
      await waitWithTimeout(_authReadyPromise, AUTH_READY_TIMEOUT_MS);
    } catch {
      // fall through; backend 401 is handled as a normal stream error
    }
  }

  const baseURL = api.defaults.baseURL || '';
  let token: string | null = _tokenRef.current;
  let response = await fetch(`${baseURL}${path}`, init(token));

  if (response.status === 401 && !isPublicRequest(path) && !ctrl.signal.aborted) {
    const refreshed = await refreshAccessToken();
    if (refreshed === null || ctrl.signal.aborted) return response;
    token = refreshed;
    response = await fetch(`${baseURL}${path}`, init(token));
  }
  return response;
};

export const streamApiEvents = (
  path: string,
  payload: unknown,
  {
    onEvent,
    onClose,
    onError,
  }: {
    onEvent: StreamEventHandler;
    onClose?: () => void;
    onError: (err: Error) => void;
  },
): AbortController => {
  const ctrl = new AbortController();

  const run = async (): Promise<void> => {
    const response = await fetchStreamWithAuth(
      path,
      (token) => {
        const headers: Record<string, string> = { 'Content-Type': 'application/json' };
        if (token) headers.Authorization = `Bearer ${token}`;
        return {
          method: 'POST',
          headers,
          credentials: 'include',
          body: JSON.stringify(payload),
          signal: ctrl.signal,
        };
      },
      ctrl,
    );

    if (!response.ok) throw await parseStreamError(response);

    await consumeSSE(response, ctrl, (event) => onEvent(event.data), onClose);
  };

  run().catch((err: Error) => {
    // AbortError 或 abort 窗口内 refresh 返回的原 401：用户已取消流，静默。
    if (err.name !== 'AbortError' && !ctrl.signal.aborted) onError(err);
  });

  return ctrl;
};

export const streamApiGet = (
  path: string,
  { lastEventId, onEvent, onClose, onError }: {
    lastEventId?: string;
    onEvent: (event: ServerSentEvent) => boolean | void;
    onClose?: () => void;
    onError: (error: Error) => void;
  },
): AbortController => {
  const ctrl = new AbortController();
  const run = async () => {
    const response = await fetchStreamWithAuth(
      path,
      (token) => {
        const headers: Record<string, string> = { Accept: 'text/event-stream' };
        if (token) headers.Authorization = `Bearer ${token}`;
        if (lastEventId) headers['Last-Event-ID'] = lastEventId;
        return {
          method: 'GET', headers, credentials: 'include', signal: ctrl.signal,
        };
      },
      ctrl,
    );
    if (!response.ok) throw await parseStreamError(response);
    await consumeSSE(response, ctrl, onEvent, onClose);
  };
  run().catch((error: Error) => { if (error.name !== 'AbortError') onError(error); });
  return ctrl;
};

export default api;
