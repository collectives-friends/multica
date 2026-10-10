"use client";
import { useAuthStore } from "./auth";
import { bindFeatureFlagSession } from "./feature-flags/session";

import { useEffect, useState } from "react";
import { QueryClientProvider } from "@tanstack/react-query";
import { createQueryClient } from "./query-client";
import type { ReactNode } from "react";

export function QueryProvider({ children }: { children: ReactNode }) {
  const [queryClient] = useState(createQueryClient);
  useEffect(() => {
    return bindFeatureFlagSession(queryClient, useAuthStore);
  }, [queryClient]);
  return (
    <QueryClientProvider client={queryClient}>
      {children}
    </QueryClientProvider>
  );
}
