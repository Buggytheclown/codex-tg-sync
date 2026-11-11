// @vitest-environment jsdom
import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { usePRMetadataPresence } from "./usePRMetadataPresence";

let requests: { body: { client_id: string; sequence: number; active: boolean; refresh: boolean }; options: RequestInit }[];
let visible = true;
beforeEach(() => {
  vi.useFakeTimers(); visible = true;
  vi.spyOn(document, "visibilityState", "get").mockImplementation(() => visible ? "visible" : "hidden");
  requests = [];
  vi.stubGlobal("fetch", vi.fn(async (_path: string, options: RequestInit) => {
    requests.push({ body: JSON.parse(options.body as string), options });
    return { ok: true };
  }));
});
afterEach(() => { cleanup(); vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });
const settle = async () => { await act(async () => { await Promise.resolve(); }); };
const event = async (name: string, target: Document | Window = window) => {
  await act(async () => { target.dispatchEvent(new Event(name)); });
};
it("renews only a visible page, releases on hide and resumes immediately without focus", async () => {
  const { unmount } = renderHook(usePRMetadataPresence); await settle();
  expect(requests.map(r => r.body.active)).toEqual([true]);
  await act(async () => { vi.advanceTimersByTime(10000); });
  expect(requests.map(r => r.body.active)).toEqual([true, true]);
  await event("blur"); await event("focus"); expect(requests).toHaveLength(2);
  visible = false; await event("visibilitychange", document);
  expect(requests.at(-1)?.body.active).toBe(false);
  expect(requests.at(-1)?.options.keepalive).toBe(true);
  await act(async () => { vi.advanceTimersByTime(60000); }); expect(requests).toHaveLength(3);
  visible = true; await event("visibilitychange", document);
  expect(requests.at(-1)?.body.active).toBe(true);
  expect(requests.map(r => r.body.sequence)).toEqual([1, 2, 3, 4]);
  expect(new Set(requests.map(r => r.body.client_id)).size).toBe(1);
  unmount(); await settle(); expect(requests.at(-1)?.body.active).toBe(false);
  expect(vi.getTimerCount()).toBe(0);
});
it("does not register a hidden page as active and handles page lifecycle", async () => {
  visible = false; renderHook(usePRMetadataPresence); await settle();
  expect(requests.some(r => r.body.active)).toBe(false);
  visible = true; await event("pageshow"); expect(requests.at(-1)?.body.active).toBe(true);
  await event("pagehide"); expect(requests.at(-1)?.body.active).toBe(false);
  await act(async () => { vi.advanceTimersByTime(20000); });
  expect(requests.filter(r => r.body.active)).toHaveLength(1);
  await event("pageshow"); expect(requests.at(-1)?.body.active).toBe(true);
});
it("requests a refresh after reconnection without granting hidden pages activity", async () => {
  renderHook(usePRMetadataPresence); await settle(); await event("online");
  expect(requests.at(-1)?.body).toMatchObject({ active: true, refresh: true });
  visible = false; await event("visibilitychange", document);
  const count = requests.length; await event("online"); expect(requests).toHaveLength(count);
});
it("retries failed renewals on the next heartbeat with refresh and bounds a hung request", async () => {
  const fetchMock = vi.mocked(fetch); fetchMock.mockRejectedValueOnce(new Error("offline"));
  renderHook(usePRMetadataPresence); await settle();
  await act(async () => { vi.advanceTimersByTime(10000); }); expect(requests.at(-1)?.body.refresh).toBe(true);
  let signal: AbortSignal | null | undefined;
  fetchMock.mockImplementationOnce((_url, options) => { signal = options?.signal; return new Promise(() => {}); });
  await act(async () => { vi.advanceTimersByTime(10000); }); expect(signal?.aborted).toBe(false);
  await act(async () => { vi.advanceTimersByTime(5000); }); expect(signal?.aborted).toBe(true);
});
it("uses distinct identities for two mounted pages", async () => {
  renderHook(usePRMetadataPresence); renderHook(usePRMetadataPresence); await settle();
  expect(new Set(requests.map(r => r.body.client_id)).size).toBe(2);
  expect(requests.every(r => !r.body.refresh)).toBe(true);
});
