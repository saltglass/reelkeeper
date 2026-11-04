import { type ComponentProps, useLayoutEffect, useRef, useState } from "react";
import { cn } from "@/lib/utils";

/**
 * An image that develops: it stays clear until its picture has arrived, then
 * fades up (index.css, `.develop`), like a print in the tray. Without it a
 * poster pops in after its frame has already faded in, which reads as a
 * flicker rather than a fade. The fade follows Settings → Appearance →
 * Motion; with Motion off the picture simply appears.
 */
export function DevelopImg({ className, onLoad, onError, ...props }: ComponentProps<"img">) {
  const ref = useRef<HTMLImageElement>(null);
  const [developed, setDeveloped] = useState(false);
  // A picture already in the browser's cache can finish before React
  // attaches onLoad; it is shown straight away.
  useLayoutEffect(() => {
    if (ref.current?.complete && ref.current.naturalWidth > 0) setDeveloped(true);
  }, []);
  return (
    // biome-ignore lint/a11y/useAltText: alt comes in through props, as on a plain <img>
    <img
      ref={ref}
      {...props}
      data-developed={developed || undefined}
      className={cn("develop", className)}
      onLoad={(e) => {
        setDeveloped(true);
        onLoad?.(e);
      }}
      onError={(e) => {
        // Nothing to fade up; don't leave a clear box over the frame's own look.
        setDeveloped(true);
        onError?.(e);
      }}
    />
  );
}
