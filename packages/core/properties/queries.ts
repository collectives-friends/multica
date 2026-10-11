import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export type PropertyResourceType = "issue" | "project";

export const propertyKeys = {
  all: (wsId: string) => ["properties", wsId] as const,
  list: (wsId: string, includeArchived = false, resourceType: PropertyResourceType = "issue") =>
    [...propertyKeys.all(wsId), "list", includeArchived, resourceType] as const,
};

export function propertyListOptions(
  wsId: string,
  includeArchived = false,
  resourceType: PropertyResourceType = "issue",
) {
  return queryOptions({
    queryKey: propertyKeys.list(wsId, includeArchived, resourceType),
    queryFn: () => api.listProperties(includeArchived, resourceType),
    select: (data) => data.properties,
  });
}
