import { Switch as SwitchPrimitive } from "radix-ui";
import type * as React from "react";
import { cn } from "@/lib/utils";

/** A 46x28 square switch: orange when on. */
export function Switch({ className, ...props }: React.ComponentProps<typeof SwitchPrimitive.Root>) {
  return (
    <SwitchPrimitive.Root
      className={cn(
        "group peer relative inline-flex h-7 w-[46px] shrink-0 cursor-pointer items-center border border-input bg-muted p-0 transition-colors duration-[calc(240ms*var(--m))] data-[state=checked]:border-primary data-[state=checked]:bg-primary disabled:cursor-not-allowed disabled:opacity-50",
        className,
      )}
      {...props}
    >
      <SwitchPrimitive.Thumb className="pointer-events-none block size-[18px] translate-x-[3px] bg-muted-foreground transition-[transform,background-color,width] duration-[calc(380ms*var(--m))] data-[state=checked]:translate-x-[21px] data-[state=checked]:bg-primary-foreground" />
    </SwitchPrimitive.Root>
  );
}
