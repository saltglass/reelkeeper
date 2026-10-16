import { useMutation, useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { Search } from "lucide-react";
import { type FormEvent, useEffect, useState } from "react";
import { api, RequestError, unwrap } from "@/api/client";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import {
  profilesQuery,
  rootsQuery,
  type SeriesLookupResult,
  useInvalidateLibrary,
} from "@/hooks/library";
import { tmdbImage } from "@/lib/images";

/** Search TMDB and add a series (or another version of one). */
export function AddSeriesDialog({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
}) {
  const [q, setQ] = useState("");
  const [term, setTerm] = useState("");
  const [picked, setPicked] = useState<SeriesLookupResult | null>(null);

  useEffect(() => {
    const t = setTimeout(() => setTerm(q.trim()), 400);
    return () => clearTimeout(t);
  }, [q]);

  const results = useQuery({
    queryKey: ["series", "lookup", term],
    queryFn: () => unwrap(api.GET("/series/lookup", { params: { query: { q: term } } })),
    enabled: open && term.length >= 2,
    staleTime: 60_000,
  });

  function close(o: boolean) {
    onOpenChange(o);
    if (!o) {
      setQ("");
      setTerm("");
      setPicked(null);
    }
  }

  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent className="max-w-xl">
        <DialogTitle>Add a series</DialogTitle>
        <DialogDescription>
          Search TMDB by title. Add a year to narrow it down, like "Doctor Who 2005".
        </DialogDescription>
        {picked ? (
          <AddForm series={picked} onBack={() => setPicked(null)} onDone={() => close(false)} />
        ) : (
          <>
            <div className="relative">
              <Search className="pointer-events-none absolute top-2.5 left-3 size-4 text-muted-foreground" />
              <Input
                autoFocus
                aria-label="Series title"
                placeholder="Series title"
                className="pl-9"
                value={q}
                onChange={(e) => setQ(e.target.value)}
              />
            </div>
            {results.error && <Alert variant="error">{results.error.message}</Alert>}
            {results.isFetching && <p className="text-sm text-muted-foreground">Searching…</p>}
            {results.data && results.data.length === 0 && (
              <p className="text-sm text-muted-foreground">Nothing found on TMDB.</p>
            )}
            <ul className="max-h-96 space-y-1 overflow-y-auto">
              {results.data?.map((r) => (
                <li key={r.tmdbId}>
                  <button
                    type="button"
                    className="flex w-full items-start gap-3 rounded-lg p-2 text-left transition-colors duration-[calc(200ms*var(--m))] hover:bg-muted"
                    onClick={() => setPicked(r)}
                  >
                    <div className="h-18 w-12 shrink-0 overflow-hidden rounded-md border bg-muted">
                      {r.posterPath && (
                        <img
                          src={tmdbImage(r.posterPath, "w185")}
                          alt=""
                          className="size-full object-cover"
                        />
                      )}
                    </div>
                    <div className="min-w-0 space-y-1">
                      <p className="text-sm font-medium">
                        {r.title}{" "}
                        {r.year > 0 && <span className="text-muted-foreground">({r.year})</span>}
                      </p>
                      {r.inLibrary && <Badge variant="info">In your library</Badge>}
                      <p className="line-clamp-2 text-xs text-muted-foreground">{r.overview}</p>
                    </div>
                  </button>
                </li>
              ))}
            </ul>
          </>
        )}
      </DialogContent>
    </Dialog>
  );
}

function AddForm({
  series,
  onBack,
  onDone,
}: {
  series: SeriesLookupResult;
  onBack: () => void;
  onDone: () => void;
}) {
  const profiles = useQuery(profilesQuery);
  const roots = useQuery(rootsQuery);
  const invalidate = useInvalidateLibrary();
  const navigate = useNavigate();
  const tvRoots = (roots.data ?? []).filter((r) => r.mediaType === "series");
  const [profileId, setProfileId] = useState<number | "">("");
  const [rootId, setRootId] = useState<number | "">("");

  // Default the folder to the first one using the chosen profile.
  useEffect(() => {
    const first = profiles.data?.find((p) => p.appliesTo !== "movie");
    if (profileId === "" && first) setProfileId(first.id);
  }, [profiles.data, profileId]);
  useEffect(() => {
    if (profileId === "" || rootId !== "") return;
    const match = tvRoots.find((r) => r.defaultProfileId === profileId) ?? tvRoots[0];
    if (match) setRootId(match.id);
  }, [profileId, rootId, tvRoots]);

  const add = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST("/series", {
          body: {
            tmdbId: series.tmdbId,
            profileId: Number(profileId),
            rootFolderId: Number(rootId),
            monitored: true,
          },
        }),
      ),
    onSuccess: (m) => {
      invalidate();
      onDone();
      navigate({ to: "/series/$id", params: { id: String(m.id) } });
    },
  });
  const err = add.error instanceof RequestError ? add.error : undefined;

  function submit(e: FormEvent) {
    e.preventDefault();
    add.mutate();
  }

  if (tvRoots.length === 0) {
    return (
      <Alert variant="warning">
        Add a TV folder in Settings → Media Folders first, so Reelkeeper knows where this series
        would go.
      </Alert>
    );
  }

  return (
    <form onSubmit={submit} className="space-y-4">
      <p className="font-medium">
        {series.title} {series.year > 0 && `(${series.year})`}
      </p>
      {err && !Object.keys(err.fields).length && <Alert variant="error">{err.message}</Alert>}
      <Field
        id="add-profile"
        label="Quality profile"
        help="Which version of the series to add. You can add another version later."
        error={err?.fields.profileId}
      >
        <select
          id="add-profile"
          className="w-full min-h-10 rounded-lg border border-input bg-background px-3 text-sm transition-[border-color,box-shadow] duration-[calc(200ms*var(--m))] focus:border-ring focus:ring-4 focus:ring-ring/20 focus-visible:outline-none"
          value={profileId}
          onChange={(e) => {
            setProfileId(Number(e.target.value));
            setRootId("");
          }}
        >
          {profiles.data
            ?.filter((p) => p.appliesTo !== "movie")
            .map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
        </select>
      </Field>
      <Field id="add-root" label="Media folder" error={err?.fields.rootFolderId}>
        <select
          id="add-root"
          className="w-full min-h-10 rounded-lg border border-input bg-background px-3 text-sm transition-[border-color,box-shadow] duration-[calc(200ms*var(--m))] focus:border-ring focus:ring-4 focus:ring-ring/20 focus-visible:outline-none"
          value={rootId}
          onChange={(e) => setRootId(Number(e.target.value))}
        >
          {tvRoots.map((r) => (
            <option key={r.id} value={r.id}>
              {r.path}
            </option>
          ))}
        </select>
      </Field>
      <p className="text-xs text-muted-foreground">
        The series is added with all its episodes wanted. Searching for and downloading them arrive
        in a later update.
      </p>
      <div className="flex justify-between">
        <Button type="button" variant="ghost" onClick={onBack}>
          Back
        </Button>
        <Button type="submit" disabled={add.isPending || profileId === "" || rootId === ""}>
          {add.isPending ? "Adding…" : "Add series"}
        </Button>
      </div>
    </form>
  );
}
