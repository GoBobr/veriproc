import { useEffect, useState } from "preact/hooks";

// usePoll runs `fn` immediately and every `intervalMs` thereafter. It
// returns {data, error, loading, refresh}. Designed for dashboard polling.
export function usePoll<T>(fn: () => Promise<T>, intervalMs: number, deps: unknown[] = []) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<Error | null>(null);
  const [loading, setLoading] = useState(true);
  const [tick, setTick] = useState(0);

  useEffect(() => {
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | null = null;
    const run = async () => {
      try {
        const v = await fn();
        if (!cancelled) {
          setData(v);
          setError(null);
        }
      } catch (e) {
        if (!cancelled) setError(e as Error);
      } finally {
        if (!cancelled) setLoading(false);
      }
    };
    run();
    if (intervalMs > 0) {
      const schedule = () => {
        timer = setTimeout(async () => {
          await run();
          if (!cancelled) schedule();
        }, intervalMs);
      };
      schedule();
    }
    return () => {
      cancelled = true;
      if (timer) clearTimeout(timer);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tick, intervalMs, ...deps]);

  return { data, error, loading, refresh: () => setTick((t) => t + 1) };
}

// useFetch is a one-shot variant for non-polling reads.
export function useFetch<T>(fn: () => Promise<T>, deps: unknown[] = []) {
  return usePoll(fn, 0, deps);
}
