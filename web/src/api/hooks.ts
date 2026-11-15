import {
  keepPreviousData,
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { REAUTH_HEADER, api, unwrap } from "./client";
import type { SandboxState } from "./client";
import { toApiError } from "./errors";
import type { components } from "./schema";

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

export const sessionKey = ["session"] as const;

/** The session, including the CSRF token writes must send. Held in memory only. */
export const sessionQuery = {
  queryKey: sessionKey,
  queryFn: async () => unwrap(await api.GET("/v1/sessions/current")),
  staleTime: 5 * 60_000,
} as const;

export function useSession() {
  return useQuery(sessionQuery);
}

export function useTemplates(enabled: boolean) {
  return useQuery({
    queryKey: ["templates"] as const,
    enabled,
    queryFn: async () => unwrap(await api.GET("/v1/templates")),
    staleTime: 60_000,
  });
}

export function useSnapshots() {
  return useInfiniteQuery({
    queryKey: ["snapshots"] as const,
    initialPageParam: "",
    queryFn: async ({ pageParam }) => {
      const query: { limit: number; cursor?: string } = { limit: 100 };
      if (pageParam) query.cursor = pageParam;
      return unwrap(await api.GET("/v1/snapshots", { params: { query } }));
    },
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  });
}

export function useKeys(enabled: boolean) {
  return useInfiniteQuery({
    queryKey: ["keys"] as const,
    enabled,
    initialPageParam: "",
    queryFn: async ({ pageParam }) => {
      const query: { limit: number; cursor?: string } = { limit: 100 };
      if (pageParam) query.cursor = pageParam;
      return unwrap(await api.GET("/v1/keys", { params: { query } }));
    },
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  });
}

export interface AuditFilters {
  action: string;
  sandbox_id: string;
  key_id: string;
}

export function useAudit(filters: AuditFilters, enabled: boolean) {
  return useInfiniteQuery({
    queryKey: ["audit", filters.action, filters.sandbox_id, filters.key_id] as const,
    enabled,
    initialPageParam: "",
    placeholderData: keepPreviousData,
    queryFn: async ({ pageParam }) => {
      const query: {
        limit: number;
        cursor?: string;
        action?: string;
        sandbox_id?: string;
        key_id?: string;
      } = { limit: 100 };
      if (pageParam) query.cursor = pageParam;
      if (filters.action) query.action = filters.action;
      if (filters.sandbox_id) query.sandbox_id = filters.sandbox_id;
      if (filters.key_id) query.key_id = filters.key_id;
      return unwrap(await api.GET("/v1/audit", { params: { query } }));
    },
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  });
}

/** Mutations invalidate what they change; the live event stream also keeps sandbox lists fresh. */
export function useCreateSandbox() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: async (body: components["schemas"]["CreateSandbox"]) =>
      unwrap(await api.POST("/v1/sandboxes", { body })),
    onSuccess: () => void client.invalidateQueries({ queryKey: ["sandboxes"] }),
  });
}

export function useDestroySandbox(id: string) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: async () => {
      const result = await api.DELETE("/v1/sandboxes/{id}", { params: { path: { id } } });
      if (result.error !== undefined) throw toApiError(result.response.status, result.error);
    },
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ["sandboxes"] });
      void client.invalidateQueries({ queryKey: queryKeys.sandbox(id) });
      void client.invalidateQueries({ queryKey: queryKeys.overview });
    },
  });
}

export function useKeepalive(id: string) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: async () => {
      const result = await api.POST("/v1/sandboxes/{id}/keepalive", { params: { path: { id } } });
      if (result.error !== undefined) throw toApiError(result.response.status, result.error);
    },
    onSuccess: () => void client.invalidateQueries({ queryKey: queryKeys.sandbox(id) }),
  });
}

export function useCreateSnapshot(id: string) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: async () =>
      unwrap(await api.POST("/v1/sandboxes/{id}/snapshots", { params: { path: { id } } })),
    onSuccess: () => void client.invalidateQueries({ queryKey: ["snapshots"] }),
  });
}

export function useForkSnapshot(id: string) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: async (count: number) =>
      unwrap(
        await api.POST("/v1/snapshots/{id}/fork", { params: { path: { id } }, body: { count } }),
      ),
    onSuccess: () => void client.invalidateQueries({ queryKey: ["sandboxes"] }),
  });
}

export function useDeleteSnapshot() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) => {
      const result = await api.DELETE("/v1/snapshots/{id}", { params: { path: { id } } });
      if (result.error !== undefined) throw toApiError(result.response.status, result.error);
    },
    onSuccess: () => void client.invalidateQueries({ queryKey: ["snapshots"] }),
  });
}

/**
 * Key management needs the admin key typed again: it travels in one request header and is never
 * kept in state, storage or the query cache.
 */
export function useCreateKey() {
  const client = useQueryClient();
  return useMutation({
    // The result holds the new key's secret: drop it from the cache as soon as nothing observes it.
    gcTime: 0,
    mutationFn: async (input: { name: string; isAdmin: boolean; adminKey: string }) =>
      unwrap(
        await api.POST("/v1/keys", {
          body: { name: input.name, is_admin: input.isAdmin },
          headers: { [REAUTH_HEADER]: input.adminKey.trim() },
        }),
      ),
    onSuccess: () => void client.invalidateQueries({ queryKey: ["keys"] }),
  });
}

export function useRevokeKey() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: async (input: { id: string; adminKey: string }) => {
      const result = await api.DELETE("/v1/keys/{id}", {
        params: { path: { id: input.id } },
        headers: { [REAUTH_HEADER]: input.adminKey.trim() },
      });
      if (result.error !== undefined) throw toApiError(result.response.status, result.error);
    },
    onSuccess: () => void client.invalidateQueries({ queryKey: ["keys"] }),
  });
}
