import { useCallback, useEffect, useState } from "react";

/**
 * An editable copy of server data for a settings form.
 *
 * - `dirty` is true once the user has changed something.
 * - When the server data changes (saved in another tab, say) and the user
 *   hasn't edited anything, the form follows it. Unsaved edits are never
 *   overwritten.
 * - `reset()` throws edits away; `commit(saved)` adopts what the server
 *   stored after a save.
 */
export function useDraft<T>(data: T | undefined, equal: (a: T, b: T) => boolean) {
  // base: the server value the draft started from.
  const [state, setState] = useState<{ base?: T; draft?: T }>({ base: data, draft: data });

  useEffect(() => {
    if (data === undefined) return;
    setState((s) => ({
      base: data,
      draft:
        s.draft === undefined || s.base === undefined || equal(s.draft, s.base) ? data : s.draft,
    }));
  }, [data, equal]);

  const setDraft = useCallback((draft: T) => setState((s) => ({ ...s, draft })), []);
  const reset = useCallback(() => setState((s) => ({ ...s, draft: s.base })), []);
  const commit = useCallback((saved: T) => setState({ base: saved, draft: saved }), []);

  const { base, draft } = state;
  const dirty = draft !== undefined && base !== undefined && !equal(draft, base);
  return { draft, setDraft, dirty, reset, commit };
}
