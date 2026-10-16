import { useQuery } from "@tanstack/react-query";
import { ListPlus } from "lucide-react";
import { RequestError } from "@/api/client";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { backfillQuery, backfillStanding, useAddBackfill } from "@/hooks/backfill";
import { toast } from "@/hooks/toast";

/**
 * "Add to backfill" for one series version, or where it stands in the
 * queue ("Backfilling now", "In backfill (#3)"). `hasMissing` is false when
 * the show has no aired episodes missing: then there is nothing to add, and
 * the button is left out (a show already in the queue still says so).
 */
export function AddToBackfillButton({
  versionId,
  title,
  hasMissing = true,
}: {
  versionId: number;
  title: string;
  hasMissing?: boolean;
}) {
  const { data } = useQuery(backfillQuery);
  const add = useAddBackfill();
  if (!data) return null;
  const standing = backfillStanding(data, versionId);
  if (standing.kind === "now") return <Badge variant="info">Backfilling now</Badge>;
  if (standing.kind === "waiting")
    return <Badge variant="info">In backfill (#{standing.place})</Badge>;
  if (!hasMissing) return null;
  return (
    <Button
      variant="outline"
      size="sm"
      disabled={add.isPending}
      aria-label={`Add ${title} to backfill`}
      title="Fills in this show's older missing episodes, one show at a time (Library → Wanted → Backfill)."
      onClick={() =>
        add.mutate(versionId, {
          onSuccess: () => toast(`${title} was added to backfill.`, "ok"),
          onError: (e) =>
            toast(
              e instanceof RequestError ? e.message : "Couldn't add it to backfill.",
              "problem",
            ),
        })
      }
    >
      <ListPlus /> {add.isPending ? "Adding…" : "Add to backfill"}
    </Button>
  );
}
