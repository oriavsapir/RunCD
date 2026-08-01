import { useEffect } from "react";

// Calls onTick every intervalMs, skipping ticks while the tab is hidden —
// a backgrounded tab has no visible UI to refresh, so polling it anyway is
// wasted network/DB traffic. Catches up immediately on the tick after the
// tab becomes visible again, via the visibilitychange listener, rather than
// waiting out however much of the interval was already hidden.
export function usePolling(intervalMs: number, onTick: () => void) {
  useEffect(() => {
    const tick = () => {
      if (!document.hidden) onTick();
    };
    const id = setInterval(tick, intervalMs);
    document.addEventListener("visibilitychange", tick);
    return () => {
      clearInterval(id);
      document.removeEventListener("visibilitychange", tick);
    };
    // onTick is intentionally excluded: every caller passes a setState
    // updater shaped like `(k) => k + 1`, stable in practice, and
    // re-subscribing the interval/listener on every render would just
    // churn the same subscription for no behavior change.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [intervalMs]);
}
