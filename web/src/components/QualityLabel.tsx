import type * as React from "react";
import { Badge } from "@/components/ui/badge";

/**
 * A version's quality label (the badges under each movie). Long labels such
 * as "Remux · Remux-2160p · DV · HDR10" are wider than a poster, so they wrap
 * inside the badge instead of spilling over.
 */
export function QualityLabel(props: React.ComponentProps<typeof Badge>) {
  return <Badge className="max-w-full rounded-md whitespace-normal wrap-anywhere" {...props} />;
}
