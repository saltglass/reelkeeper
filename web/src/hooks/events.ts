import { useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";
import { applyJobEvent, type Job } from "@/hooks/tasks";
import { toast } from "@/hooks/toast";
import { urlBase } from "@/lib/utils";

/**
 * Listens to the server's live event stream and refreshes affected data,
 * so changes made in another tab or by another device show up at once.
 * The browser reconnects automatically if the stream drops.
 */
export function useLiveEvents(enabled: boolean) {
  const qc = useQueryClient();
  useEffect(() => {
    if (!enabled || typeof EventSource === "undefined") return;
    const es = new EventSource(`${urlBase()}/api/v1/events`);

    es.addEventListener("settings.changed", (e) => {
      const { group } = JSON.parse((e as MessageEvent).data) as { group?: string };
      if (group) qc.invalidateQueries({ queryKey: ["settings", group] });
      qc.invalidateQueries({ queryKey: ["auth"] });
    });
    es.addEventListener("task.updated", (e) => {
      const job = JSON.parse((e as MessageEvent).data) as Job;
      applyJobEvent(qc, job);
      // The daily backup finished: there is a new file in the list.
      if (job.kind === "backup.scheduled" && ["succeeded", "failed"].includes(job.status)) {
        qc.invalidateQueries({ queryKey: ["backups"] });
      }
      // A finished search changes what Wanted says about its movie or show.
      if (job.kind === "grab.search" && ["succeeded", "failed"].includes(job.status)) {
        qc.invalidateQueries({ queryKey: ["wanted"] });
      }
      // Likewise for the backfill queue's turn.
      if (job.kind === "grab.backfill" && ["succeeded", "failed"].includes(job.status)) {
        qc.invalidateQueries({ queryKey: ["backfill"] });
        qc.invalidateQueries({ queryKey: ["wanted"] });
      }
    });
    es.addEventListener("library.changed", () => {
      qc.invalidateQueries({ queryKey: ["movies"] });
      qc.invalidateQueries({ queryKey: ["series"] });
      qc.invalidateQueries({ queryKey: ["library"] });
      qc.invalidateQueries({ queryKey: ["wanted"] });
      qc.invalidateQueries({ queryKey: ["backfill"] });
    });
    // Something worth a pop-up happened (an import finished, a download failed).
    es.addEventListener("notice", (e) => {
      const n = JSON.parse((e as MessageEvent).data) as {
        message?: string;
        title?: string;
        problem?: boolean;
      };
      const text = n.message || n.title;
      if (text) toast(text, n.problem ? "problem" : "ok");
    });
    es.addEventListener("session.ended", () => {
      qc.invalidateQueries({ queryKey: ["auth"] });
    });
    // After a reconnect, anything may have changed while we were away.
    es.addEventListener("open", () => qc.invalidateQueries());

    return () => es.close();
  }, [enabled, qc]);
}
