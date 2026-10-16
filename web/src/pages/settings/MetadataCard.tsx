import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useEffect, useState } from "react";
import { api, RequestError, unwrap } from "@/api/client";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";

const query = {
  queryKey: ["settings", "metadata"],
  queryFn: () => unwrap(api.GET("/settings/metadata")),
};

/** TMDB key and language (Settings → General). */
export function MetadataCard() {
  const { data } = useQuery(query);
  const qc = useQueryClient();
  const [key, setKey] = useState("");
  const [language, setLanguage] = useState("en-US");
  useEffect(() => {
    if (data) setLanguage(data.language);
  }, [data]);

  const save = useMutation({
    mutationFn: () =>
      unwrap(
        api.PUT("/settings/metadata", { body: { language, ...(key ? { tmdbApiKey: key } : {}) } }),
      ),
    onSuccess: (d) => {
      qc.setQueryData(query.queryKey, d);
      setKey("");
    },
  });
  const check = useMutation({
    mutationFn: () =>
      unwrap(api.POST("/settings/metadata/check", { body: key ? { tmdbApiKey: key } : {} })),
  });
  const err = save.error instanceof RequestError ? save.error : undefined;

  function submit(e: FormEvent) {
    e.preventDefault();
    save.mutate();
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Movie information (TMDB)</CardTitle>
        <CardDescription>
          Reelkeeper identifies movies and fetches their details from The Movie Database. It needs a
          free API key: create an account at themoviedb.org, then copy the key from Settings → API.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <form onSubmit={submit} className="space-y-5">
          {err && !Object.keys(err.fields).length && <Alert variant="error">{err.message}</Alert>}
          {data && !data.tmdbApiKeySet && (
            <Alert variant="warning">
              No key yet: scanning can't identify new movies until one is added.
            </Alert>
          )}
          <Field
            id="tmdb-key"
            label="API key"
            help={
              data?.tmdbApiKeySet
                ? "A key is stored. Leave this empty to keep it."
                : 'The "API Key" or the "API Read Access Token".'
            }
            error={err?.fields.tmdbApiKey}
          >
            <Input
              id="tmdb-key"
              type="password"
              autoComplete="off"
              placeholder={data?.tmdbApiKeySet ? "••••••••" : ""}
              value={key}
              onChange={(e) => {
                setKey(e.target.value);
                check.reset();
              }}
            />
          </Field>
          <Field
            id="tmdb-lang"
            label="Language"
            help="For titles and descriptions, as a code like en-US or de-DE."
            error={err?.fields.language}
          >
            <Input
              id="tmdb-lang"
              value={language}
              onChange={(e) => setLanguage(e.target.value)}
              className="w-40"
            />
          </Field>
          {check.data && (
            <Alert variant={check.data.ok ? "success" : "error"}>{check.data.message}</Alert>
          )}
          <div className="flex justify-end gap-2">
            <Button
              type="button"
              variant="outline"
              disabled={check.isPending}
              onClick={() => check.mutate()}
            >
              {check.isPending ? "Checking…" : "Check key"}
            </Button>
            <Button type="submit" disabled={save.isPending}>
              {save.isPending ? "Saving…" : save.isSuccess ? "Saved" : "Save"}
            </Button>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}
