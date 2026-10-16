import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api, unwrap } from "@/api/client";
import type { components } from "@/api/schema";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";
import { relativeTime } from "@/lib/time";

type SkippedFolder = components["schemas"]["SkippedFolder"];

const skippedQuery = {
  queryKey: ["library", "skipped"],
  queryFn: () => unwrap(api.GET("/skipped-folders")),
};

/** Folders of deleted movies whose files were kept; scans leave them alone. */
export function SkippedFoldersCard() {
  const { data } = useQuery(skippedQuery);
  const qc = useQueryClient();
  const [confirming, setConfirming] = useState<SkippedFolder>();
  const unskip = useMutation({
    mutationFn: (id: number) =>
      unwrap(api.DELETE("/skipped-folders/{id}", { params: { path: { id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["library"] }),
  });

  if (!data || data.length === 0) return null;
  return (
    <Card>
      <CardHeader>
        <CardTitle>Skipped folders</CardTitle>
        <CardDescription>
          You deleted these movies from Reelkeeper but kept their files, so scans leave their
          folders alone. Stop skipping a folder to have Reelkeeper find its movie again.
        </CardDescription>
      </CardHeader>
      <CardContent>
        {unskip.error && <Alert variant="error">{unskip.error.message}</Alert>}
        <ul className="divide-y">
          {data.map((f) => (
            <li key={f.id} className="flex flex-wrap items-center gap-3 py-2">
              <div className="mr-auto min-w-0">
                <p className="font-mono text-sm break-all">
                  {f.rootPath}/{f.path}
                </p>
                <p className="text-xs text-muted-foreground">
                  {f.title} · skipped {relativeTime(f.createdAt)}
                </p>
              </div>
              <Button variant="outline" size="sm" onClick={() => setConfirming(f)}>
                Stop skipping
              </Button>
            </li>
          ))}
        </ul>
      </CardContent>
      <Dialog open={Boolean(confirming)} onOpenChange={(o) => !o && setConfirming(undefined)}>
        <DialogContent>
          <DialogTitle>Stop skipping this folder?</DialogTitle>
          <DialogDescription>
            The next scan will add what's in this folder back to Reelkeeper.
          </DialogDescription>
          <div className="flex justify-end gap-2">
            <DialogClose asChild>
              <Button variant="ghost">Cancel</Button>
            </DialogClose>
            <Button
              onClick={() => {
                if (confirming) unskip.mutate(confirming.id);
                setConfirming(undefined);
              }}
            >
              Stop skipping
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </Card>
  );
}
