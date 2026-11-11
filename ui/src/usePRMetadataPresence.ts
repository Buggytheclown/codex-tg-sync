import { useCallback, useEffect, useRef } from "react";

// This permission controls passive Arc reads, never task polling or launches.
export function usePRMetadataPresence(): () => void {
  const reconnect = useRef<() => void>(() => {});
  const onReconnect = useCallback(() => reconnect.current(), []);
  useEffect(() => {
    const clientID = crypto.randomUUID();
    let sequence = 0, pageHidden = false, wasActive = false, needsRefresh = false;
    let pending: { controller: AbortController; timer: number } | undefined;
    const active = () => !pageHidden && document.visibilityState === "visible";
    const cancelPending = () => {
      if (!pending) return;
      window.clearTimeout(pending.timer);
      pending.controller.abort();
      pending = undefined;
    };
    const send = (isActive: boolean, refresh = false) => {
      cancelPending();
      const sentSequence = ++sequence;
      const controller = new AbortController();
      const timer = window.setTimeout(() => { controller.abort(); }, 5000);
      pending = { controller, timer };
      void fetch("/v1/board-presence", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ client_id: clientID, sequence: sentSequence, active: isActive, refresh: isActive && refresh }),
        signal: controller.signal,
        keepalive: !isActive,
      }).then(response => {
        if (sentSequence === sequence) needsRefresh = !response.ok;
      }).catch(() => {
        if (sentSequence === sequence) needsRefresh = true;
      }).finally(() => {
        window.clearTimeout(timer);
        if (pending?.controller === controller) pending = undefined;
      });
    };
    const syncVisibility = () => {
      const isActive = active();
      if (isActive === wasActive) return;
      wasActive = isActive;
      send(isActive, needsRefresh);
    };
    const pageHide = () => { pageHidden = true; syncVisibility(); };
    const pageShow = () => { pageHidden = false; syncVisibility(); };
    const online = () => { if (active()) send(true, true); };
    reconnect.current = online;
    document.addEventListener("visibilitychange", syncVisibility);
    window.addEventListener("pagehide", pageHide);
    window.addEventListener("pageshow", pageShow);
    window.addEventListener("online", online);
    const heartbeat = window.setInterval(() => { if (active()) send(true, needsRefresh); }, 10000);
    syncVisibility();
    return () => {
      reconnect.current = () => {};
      document.removeEventListener("visibilitychange", syncVisibility);
      window.removeEventListener("pagehide", pageHide);
      window.removeEventListener("pageshow", pageShow);
      window.removeEventListener("online", online);
      window.clearInterval(heartbeat);
      cancelPending();
      if (wasActive) send(false);
    };
  }, []);
  return onReconnect;
}
