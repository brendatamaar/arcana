import { keepPreviousData, useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { api, unwrap } from "./client";
import type { SandboxState } from "./client";

export interface SandboxFilters {
  state: SandboxState | "";
  /** `key:value`, applied by the server. */
  label: string;
}

export const queryKeys = {
  overview: ["overview"] as const,
  sandboxes: (f: SandboxFilters) => ["sandboxes", f.state, f.label] as const,
  sandbox: (id: string) => ["sandbox", id] as const,
  history: (id: string) => ["history", id] as const,
  execs: (id: string) => ["execs", id] as const,
};

export function useOverview() {
  return useQuery({
    queryKey: queryKeys.overview,
    queryFn: async () => unwrap(await api.GET("/v1/overview")),
  });
}

export function useSandboxes(filters: SandboxFilters) {
  return useInfiniteQuery({
    queryKey: queryKeys.sandboxes(filters),
    initialPageParam: "",
    placeholderData: keepPreviousData,
    queryFn: async ({ pageParam }) => {
      const query: { limit: number; cursor?: string; state?: SandboxState; label?: string } = {
        limit: 100,
      };
      if (pageParam) query.cursor = pageParam;
      if (filters.state) query.state = filters.state;
      if (filters.label) query.label = filters.label;
      return unwrap(await api.GET("/v1/sandboxes", { params: { query } }));
    },
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  });
}

export function useSandbox(id: string) {
  return useQuery({
    queryKey: queryKeys.sandbox(id),
    queryFn: async () => unwrap(await api.GET("/v1/sandboxes/{id}", { params: { path: { id } } })),
  });
}

export function useHistory(id: string) {
  return useQuery({
    queryKey: queryKeys.history(id),
    queryFn: async () =>
      unwrap(await api.GET("/v1/sandboxes/{id}/history", { params: { path: { id } } })),
  });
}

export function useExecs(id: string) {
  return useQuery({
    queryKey: queryKeys.execs(id),
    queryFn: async () =>
      unwrap(
        await api.GET("/v1/sandboxes/{id}/execs", {
          params: { path: { id }, query: { limit: 50 } },
        }),
      ),
  });
}
