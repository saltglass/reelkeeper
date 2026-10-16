import { Link } from "@tanstack/react-router";
import { Compass } from "lucide-react";
import { Button } from "@/components/ui/button";

export function NotFoundPage() {
  return (
    <div className="flex min-h-full flex-col items-center justify-center gap-3 p-8 text-center">
      <span className="relative mb-2 grid size-28 place-items-center rounded-full bg-accent text-accent-foreground motion-safe:animate-[float_calc(4s*var(--m))_ease-in-out_infinite]">
        <span
          aria-hidden="true"
          className="absolute -inset-2.5 rounded-full border border-dashed border-primary/50"
        />
        <Compass className="size-10" />
      </span>
      <h1 className="text-xl font-semibold">Page not found</h1>
      <p className="text-sm text-muted-foreground">This page doesn't exist (yet).</p>
      <Button asChild variant="outline" className="mt-2">
        <Link to="/">Go to Reelkeeper</Link>
      </Button>
    </div>
  );
}
