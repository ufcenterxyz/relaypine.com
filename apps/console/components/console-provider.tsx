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
  refresh: () => Promise<void>;
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
  const refresh = useCallback(async () => {
    setRefreshing(true);
    try {
      const data = await api<Snapshot>("/api/state");
      setState(data);
      setError("");
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) {
        router.replace("/?auth=login");
      } else {
        setError(errorMessage(e));
      }
    } finally {
      setRefreshing(false);
    }
  }, [router]);
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
      <div className="mx-auto max-w-6xl p-6">
        <Brand />
        <div className="mt-14">
          {error ? (
            <ErrorBox
              message={`${t.loadError}: ${error}`}
              retry={() => void refresh()}
            />
          ) : (
            <div aria-label={t.loading}>
              <Skeleton className="mb-6 h-9 w-56 rounded-lg" />
              <div className="grid grid-cols-2 gap-4 sm:grid-cols-4">
                {[1, 2, 3, 4].map((i) => (
                  <Skeleton key={i} className="h-28 rounded-xl" />
                ))}
              </div>
              <Skeleton className="mt-8 h-72 rounded-xl" />
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
