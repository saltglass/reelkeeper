import { cva, type VariantProps } from "class-variance-authority";
import { Slot } from "radix-ui";
import type * as React from "react";
import { cn } from "@/lib/utils";

/**
 * The app's icon look: a rounded square tinted with the brand gradient and a
 * violet symbol. Icon-only buttons use it (variant "tile"), and so do the
 * icons that head settings cards.
 */
export const tileClass =
  "rounded-[10px] border bg-gradient-to-br from-brand-from/15 to-brand-to/15 text-primary";

const buttonVariants = cva(
  "inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg text-sm font-medium transition-[transform,box-shadow,background-color,border-color,color] duration-[calc(180ms*var(--m))] hover:-translate-y-px active:translate-y-0 active:scale-[0.96] disabled:pointer-events-none disabled:opacity-50 [&_svg]:size-4 [&_svg]:shrink-0 cursor-pointer",
  {
    variants: {
      variant: {
        default:
          "bg-primary text-primary-foreground hover:shadow-[0_8px_24px_-8px_color-mix(in_srgb,var(--primary)_80%,transparent)]",
        destructive: "bg-destructive text-destructive-foreground hover:bg-destructive/90",
        outline: "border border-input bg-card hover:bg-muted",
        ghost: "hover:bg-muted",
        link: "text-link underline-offset-4 hover:underline",
        tile: cn(
          tileClass,
          "hover:from-brand-from/25 hover:to-brand-to/25 aria-pressed:from-brand-from/35 aria-pressed:to-brand-to/35 aria-pressed:border-primary/60",
        ),
      },
      size: {
        default: "min-h-10 px-4",
        sm: "min-h-8 px-3 text-xs",
        lg: "min-h-11 px-6",
        icon: "size-10",
      },
    },
    defaultVariants: { variant: "default", size: "default" },
  },
);

export function Button({
  className,
  variant,
  size,
  asChild = false,
  ...props
}: React.ComponentProps<"button"> & VariantProps<typeof buttonVariants> & { asChild?: boolean }) {
  const Comp = asChild ? Slot.Root : "button";
  return <Comp className={cn(buttonVariants({ variant, size }), className)} {...props} />;
}
