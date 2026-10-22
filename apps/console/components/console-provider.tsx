"use client";
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useState,
} from "react";
import { useRouter } from "next/navigation";
import { Skeleton } from "@heroui/react";
import { api, ApiError, errorMessage } from "@/lib/api";
import type { Snapshot } from "@/lib/types";
import { ErrorBox, Brand } from "./shared";
import { en as t } from "@/i18n/en";
const StateContext = createContext<{
  state: Snapshot;
  refresh: (options?: { throwOnError?: boolean }) => Promise<void>;
  refreshing: boolean;
} | null>(null);
export function useConsole() {
  const context = useContext(StateContext);
  if (!context) throw new Error("Console provider missing");
  return context;
}
export function ConsoleProvider({ children }: { children: React.ReactNode }) {
  const router = useRouter();
  const [state, setState] = useState<Snapshot | null>(null);
  const [error, setError] = useState("");
  const [refreshing, setRefreshing] = useState(false);
  const refresh = useCallback(
    async (options?: { throwOnError?: boolean }) => {
      setRefreshing(true);
      try {
        const data = await api<Snapshot>("/api/state");
        setState(data);
        setError("");
      } catch (e) {
        if (e instanceof ApiError && e.status === 401) {
          setState(null);
          router.replace("/?auth=login");
        } else {
          setError(errorMessage(e));
        }
        if (options?.throwOnError) throw e;
      } finally {
        setRefreshing(false);
      }
    },
    [router],
  );
  useEffect(() => {
    let cancelled = false;
    void api<Snapshot>("/api/state")
      .then((data) => {
        if (!cancelled) {
          setState(data);
          setError("");
        }
      })
      .catch((e) => {
        if (cancelled) return;
        if (e instanceof ApiError && e.status === 401)
          router.replace("/?auth=login");
        else setError(errorMessage(e));
      });
    return () => {
      cancelled = true;
    };
  }, [router]);
  if (!state)
    return (
      <div className="rp-console-loading" aria-busy={!error}>
        <aside className="rp-console-loading-rail" aria-hidden="true">
          <Brand />
          <div className="rp-console-loading-nav">
            {[1, 2, 3, 4, 5].map((item) => (
              <span key={item} />
            ))}
          </div>
        </aside>
        <div className="rp-console-loading-content">
          {error ? (
            <ErrorBox
              message={`${t.loadError}: ${error}`}
              retry={() => void refresh()}
            />
          ) : (
            <div aria-label={t.loading} role="status">
              <Skeleton className="mb-4 h-9 w-56 rounded-md" />
              <Skeleton className="mb-8 h-4 w-72 max-w-full rounded-md" />
              <div className="grid grid-cols-2 gap-4 sm:grid-cols-4">
                {[1, 2, 3, 4].map((item) => (
                  <Skeleton key={item} className="h-32 rounded-[3px]" />
                ))}
              </div>
              <div className="mt-6 grid gap-5 sm:grid-cols-3">
                <Skeleton className="h-80 rounded-[3px] sm:col-span-2" />
                <Skeleton className="h-80 rounded-[3px]" />
              </div>
            </div>
          )}
        </div>
      </div>
    );
  return (
    <StateContext.Provider value={{ state, refresh, refreshing }}>
      {error && (
        <div className="fixed bottom-4 right-4 z-50 max-w-md">
          <ErrorBox message={error} retry={() => void refresh()} />
        </div>
      )}
      {children}
    </StateContext.Provider>
  );
}
