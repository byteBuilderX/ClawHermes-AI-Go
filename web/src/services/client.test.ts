import { describe, expect, it, vi } from 'vitest';

import api, {
  getTokenRef,
  markAuthReady,
  resetAuthReady,
  setupApiInterceptors,
  streamApiGet,
  streamApiEvents,
} from './client';

describe('api client', () => {
  it('does not wait for auth readiness before guest login', async () => {
    resetAuthReady();
    setupApiInterceptors({ current: null });
    const requestInterceptors = api.interceptors.request as unknown as {
      handlers: Array<{
        fulfilled?: (config: { headers: Record<string, unknown>; url: string }) => Promise<unknown>;
      }>;
    };
    const latestInterceptor = requestInterceptors.handlers[requestInterceptors.handlers.length - 1];
    let settled = false;

    void latestInterceptor?.fulfilled?.({ headers: {}, url: '/auth/guest' }).then(() => {
      settled = true;
    });
    await Promise.resolve();
    await Promise.resolve();

    expect(settled).toBe(true);
    markAuthReady();
  });

  it('waits for auth and attaches the memory token for the tenant model catalogue', async () => {
    resetAuthReady();
    const tokenRef = { current: 'model-catalogue-token' };
    setupApiInterceptors(tokenRef);
    const requestInterceptors = api.interceptors.request as unknown as {
      handlers: Array<{
        fulfilled?: (config: { headers: Record<string, unknown>; url: string }) => Promise<unknown>;
      }>;
    };
    const latestInterceptor = requestInterceptors.handlers[requestInterceptors.handlers.length - 1];
    let resolved: { headers?: Record<string, unknown> } | undefined;
    const pending = latestInterceptor?.fulfilled?.({ headers: {}, url: '/models' }).then((config) => {
      resolved = config as { headers?: Record<string, unknown> };
    });
    await Promise.resolve();
    expect(resolved).toBeUndefined();

    markAuthReady();
    await pending;
    expect(resolved?.headers).toMatchObject({ Authorization: 'Bearer model-catalogue-token' });
  });

  it('does not read or write access tokens from localStorage', async () => {
    const getItem = vi.spyOn(Storage.prototype, 'getItem');
    const setItem = vi.spyOn(Storage.prototype, 'setItem');
    const removeItem = vi.spyOn(Storage.prototype, 'removeItem');
    const tokenRef = { current: 'memory-token' };

    markAuthReady();
    setupApiInterceptors(tokenRef);
    const requestInterceptors = api.interceptors.request as unknown as {
      handlers: Array<{
        fulfilled?: (config: { headers: Record<string, unknown>; url: string }) => unknown;
      }>;
    };
    const latestInterceptor = requestInterceptors.handlers[requestInterceptors.handlers.length - 1];
    const config = await latestInterceptor?.fulfilled?.({
      headers: {},
      url: '/agents',
    }) as { headers?: Record<string, unknown> } | undefined;

    expect(config?.headers).toMatchObject({ Authorization: 'Bearer memory-token' });
    expect(getItem).not.toHaveBeenCalled();
    expect(setItem).not.toHaveBeenCalled();
    expect(removeItem).not.toHaveBeenCalled();
  });

  it('streams SSE requests with memory token auth and cookie credentials', async () => {
    getTokenRef().current = 'stream-token';
    const encoder = new TextEncoder();
    const stream = new ReadableStream({
      start(controller) {
        controller.enqueue(encoder.encode('data: {"token":"hi"}\n\n'));
        controller.close();
      },
    });
    const fetchMock = vi.fn().mockResolvedValue(new Response(stream));
    vi.stubGlobal('fetch', fetchMock);
    const onEvent = vi.fn();
    const onError = vi.fn();

    streamApiEvents('/agents/a1/execute/stream', { query: 'hello' }, { onEvent, onError });
    await vi.waitFor(() => expect(onEvent).toHaveBeenCalledWith({ token: 'hi' }));

    expect(fetchMock).toHaveBeenCalledWith(
      '/agents/a1/execute/stream',
      expect.objectContaining({
        credentials: 'include',
        headers: expect.objectContaining({
          Authorization: 'Bearer stream-token',
          'Content-Type': 'application/json',
        }),
        method: 'POST',
      }),
    );
    expect(onError).not.toHaveBeenCalled();
  });

  it('parses data from named SSE events', async () => {
    const encoder = new TextEncoder();
    const stream = new ReadableStream({
      start(controller) {
        controller.enqueue(encoder.encode(
          'event: approval_required\ndata: {"status":"waiting_approval","approvalId":"approval-1"}\n\n',
        ));
        controller.close();
      },
    });
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(stream)));
    const onEvent = vi.fn().mockReturnValue(false);
    const onClose = vi.fn();
    const onError = vi.fn();

    streamApiEvents('/agents/a1/execute/stream', { query: 'delete' }, {
      onEvent,
      onClose,
      onError,
    });

    await vi.waitFor(() => expect(onEvent).toHaveBeenCalledWith({
      status: 'waiting_approval',
      approvalId: 'approval-1',
    }));
    expect(onClose).not.toHaveBeenCalled();
    expect(onError).not.toHaveBeenCalled();
  });

  it('preserves status and public code from a failed stream request', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({
      error: '该 Agent 尚未配置可用模型',
      code: 'ASSISTANT_MODEL_UNAVAILABLE',
    }), {
      status: 503,
      headers: { 'Content-Type': 'application/json' },
    })));
    const onError = vi.fn();

    streamApiEvents('/agents/system/execute/stream', { query: 'hello' }, {
      onEvent: vi.fn(),
      onError,
    });

    await vi.waitFor(() => expect(onError).toHaveBeenCalledOnce());
    const error = onError.mock.calls[0][0] as Error & { status?: number; code?: string };
    expect(error.message).toBe('该 Agent 尚未配置可用模型');
    expect(error.status).toBe(503);
    expect(error.code).toBe('ASSISTANT_MODEL_UNAVAILABLE');
  });

  it('streams resumable GET SSE with shared auth, cookies, and Last-Event-ID', async () => {
    getTokenRef().current = 'run-token';
    const encoder = new TextEncoder();
    const stream = new ReadableStream({ start(controller) { controller.enqueue(encoder.encode('id: 9\nevent: workflow.node_completed\ndata: {"sequence_no":9}\n\n')); controller.close(); } });
    const fetchMock = vi.fn().mockResolvedValue(new Response(stream));
    vi.stubGlobal('fetch', fetchMock);
    const onEvent = vi.fn();
    streamApiGet('/workflow-runs/run-1/events/stream', { lastEventId: '8', onEvent, onError: vi.fn() });
    await vi.waitFor(() => expect(onEvent).toHaveBeenCalledWith({ id: '9', event: 'workflow.node_completed', data: { sequence_no: 9 } }));
    expect(fetchMock).toHaveBeenCalledWith('/workflow-runs/run-1/events/stream', expect.objectContaining({ method: 'GET', credentials: 'include', headers: expect.objectContaining({ Authorization: 'Bearer run-token', 'Last-Event-ID': '8' }) }));
  });

  it('refreshes once and retries SSE POST when access token expired (401)', async () => {
    markAuthReady();
    const tokenRef = { current: 'expired-token' };
    setupApiInterceptors(tokenRef, vi.fn());
    const encoder = new TextEncoder();
    const okStream = new ReadableStream({ start(controller) { controller.enqueue(encoder.encode('data: {"token":"fresh-hit"}\n\n')); controller.close(); } });
    // 首次 401（带 Authorization: Bearer expired-token），refresh 后第二次成功。
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response('', { status: 401 }))
      .mockResolvedValueOnce(new Response(okStream));
    vi.stubGlobal('fetch', fetchMock);
    const refreshSpy = vi.spyOn(api, 'post').mockResolvedValueOnce({
      data: { access_token: 'refreshed-token' },
    } as never);
    const onEvent = vi.fn();
    const onError = vi.fn();

    streamApiEvents('/agents/a1/execute/stream', { query: 'hi' }, { onEvent, onError });
    await vi.waitFor(() => expect(onEvent).toHaveBeenCalledWith({ token: 'fresh-hit' }));

    expect(refreshSpy).toHaveBeenCalledWith('/auth/refresh');
    expect(tokenRef.current).toBe('refreshed-token');
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(fetchMock).toHaveBeenLastCalledWith(
      '/agents/a1/execute/stream',
      expect.objectContaining({ headers: expect.objectContaining({ Authorization: 'Bearer refreshed-token' }) }),
    );
    expect(onError).not.toHaveBeenCalled();
    refreshSpy.mockRestore();
  });

  it('surfaces original 401 when SSE refresh fails', async () => {
    markAuthReady();
    const tokenRef = { current: 'expired-token' };
    const logout = vi.fn();
    setupApiInterceptors(tokenRef, logout);
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: 'unauthorized' }), {
      status: 401,
      headers: { 'Content-Type': 'application/json' },
    }));
    vi.stubGlobal('fetch', fetchMock);
    const refreshSpy = vi.spyOn(api, 'post').mockRejectedValueOnce(new Error('refresh token revoked'));
    const onError = vi.fn();

    streamApiEvents('/agents/a1/execute/stream', { query: 'hi' }, { onEvent: vi.fn(), onError });
    await vi.waitFor(() => expect(onError).toHaveBeenCalledOnce());

    expect(fetchMock).toHaveBeenCalledTimes(1); // refresh 失败不重放
    expect(logout).toHaveBeenCalledOnce(); // 会话失效触发登出
    const err = onError.mock.calls[0][0] as Error & { status?: number };
    expect(err.status).toBe(401);
    refreshSpy.mockRestore();
  });

  it('reuses in-flight refresh across concurrent SSE 401s (single-flight)', async () => {
    markAuthReady();
    const tokenRef = { current: 'expired-token' };
    setupApiInterceptors(tokenRef, vi.fn());
    // fetch 按 Authorization 区分：过期 token → 401，新 token → SSE 流。
    const encoder = new TextEncoder();
    const okStream = () => new ReadableStream({ start(controller) { controller.enqueue(encoder.encode('data: {"ok":true}\n\n')); controller.close(); } });
    const fetchMock = vi.fn().mockImplementation(async (_url: string, init?: RequestInit) => {
      const auth = ((init?.headers as Record<string, string>) ?? {}).Authorization;
      if (auth === 'Bearer refreshed-token') return new Response(okStream());
      return new Response('', { status: 401 });
    });
    vi.stubGlobal('fetch', fetchMock);
    const refreshSpy = vi.spyOn(api, 'post').mockResolvedValue({
      data: { access_token: 'refreshed-token' },
    } as never);
    const onEventA = vi.fn();
    const onEventB = vi.fn();
    const onError = vi.fn();

    // 两个流同时 401：应共享同一 in-flight refresh，/auth/refresh 只触发一次。
    streamApiEvents('/agents/a1/execute/stream', { query: 'a' }, { onEvent: onEventA, onError });
    streamApiEvents('/agents/a2/execute/stream', { query: 'b' }, { onEvent: onEventB, onError });
    await vi.waitFor(() => {
      expect(onEventA).toHaveBeenCalled();
      expect(onEventB).toHaveBeenCalled();
    });

    expect(refreshSpy).toHaveBeenCalledTimes(1); // 单飞：只触发一次 /auth/refresh
    expect(tokenRef.current).toBe('refreshed-token');
    expect(fetchMock).toHaveBeenCalledTimes(4); // 2 流 × (1 次 401 + 1 次成功)
    expect(onError).not.toHaveBeenCalled();
    refreshSpy.mockRestore();
  });

  it('retries SSE GET once with fresh token after 401', async () => {
    markAuthReady();
    const tokenRef = { current: 'expired-token' };
    setupApiInterceptors(tokenRef, vi.fn());
    const encoder = new TextEncoder();
    const okStream = new ReadableStream({ start(controller) { controller.enqueue(encoder.encode('data: {"sequence_no":3}\n\n')); controller.close(); } });
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response('', { status: 401 }))
      .mockResolvedValueOnce(new Response(okStream));
    vi.stubGlobal('fetch', fetchMock);
    const refreshSpy = vi.spyOn(api, 'post').mockResolvedValueOnce({ data: { access_token: 'fresh-get-token' } } as never);
    const onEvent = vi.fn();

    streamApiGet('/workflow-runs/run-1/events/stream', { onEvent, onError: vi.fn() });
    await vi.waitFor(() => expect(onEvent).toHaveBeenCalledWith({ data: { sequence_no: 3 }, event: undefined, id: undefined }));

    expect(refreshSpy).toHaveBeenCalledTimes(1);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(fetchMock).toHaveBeenLastCalledWith(
      '/workflow-runs/run-1/events/stream',
      expect.objectContaining({ headers: expect.objectContaining({ Authorization: 'Bearer fresh-get-token' }) }),
    );
    refreshSpy.mockRestore();
  });

  it('does not call onError when aborted during 401 refresh', async () => {
    markAuthReady();
    const tokenRef = { current: 'expired-token' };
    setupApiInterceptors(tokenRef, vi.fn());
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response('', { status: 401 }))
      .mockResolvedValueOnce(new Response('', { status: 200 }));
    vi.stubGlobal('fetch', fetchMock);
    const refreshSpy = vi.spyOn(api, 'post').mockResolvedValueOnce({ data: { access_token: 'x' } } as never);
    const onError = vi.fn();

    const ctrl = streamApiEvents('/agents/a1/execute/stream', { query: 'hi' }, { onEvent: vi.fn(), onError });
    ctrl.abort(); // abort 后 refresh 结果应被忽略，不触发 onError
    await new Promise((r) => setTimeout(r, 20));

    expect(onError).not.toHaveBeenCalled();
    refreshSpy.mockRestore();
  });
});
