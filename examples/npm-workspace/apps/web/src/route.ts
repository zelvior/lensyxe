import { slug } from "@ex/core";

export function routeFor(title: string): string {
  return "/" + slug(title);
}