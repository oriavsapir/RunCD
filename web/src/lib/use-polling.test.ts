import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook } from "@testing-library/react";
import { usePolling } from "./use-polling";

function setHidden(hidden: boolean) {
  Object.defineProperty(document, "hidden", {
    configurable: true,
    get: () => hidden,
  });
}

describe("usePolling", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    setHidden(false);
  });

  afterEach(() => {
    vi.useRealTimers();
    setHidden(false);
  });

  it("fires onTick on every interval while the tab is visible", () => {
    const onTick = vi.fn();
    renderHook(() => usePolling(1000, onTick));

    vi.advanceTimersByTime(3000);

    expect(onTick).toHaveBeenCalledTimes(3);
  });

  it("skips a tick while the tab is hidden", () => {
    const onTick = vi.fn();
    setHidden(true);
    renderHook(() => usePolling(1000, onTick));

    vi.advanceTimersByTime(3000);

    expect(onTick).not.toHaveBeenCalled();
  });

  it("catches up immediately when the tab becomes visible again", () => {
    const onTick = vi.fn();
    setHidden(true);
    renderHook(() => usePolling(1000, onTick));
    vi.advanceTimersByTime(3000);
    expect(onTick).not.toHaveBeenCalled();

    setHidden(false);
    document.dispatchEvent(new Event("visibilitychange"));

    expect(onTick).toHaveBeenCalledTimes(1);
  });

  it("clears the interval and removes the listener on unmount", () => {
    const onTick = vi.fn();
    const removeSpy = vi.spyOn(document, "removeEventListener");
    const { unmount } = renderHook(() => usePolling(1000, onTick));

    unmount();
    vi.advanceTimersByTime(5000);

    expect(onTick).not.toHaveBeenCalled();
    expect(removeSpy).toHaveBeenCalledWith(
      "visibilitychange",
      expect.any(Function),
    );
  });
});
