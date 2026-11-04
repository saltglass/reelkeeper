import { cva, type VariantProps } from "class-variance-authority";
import { CircleAlert, CircleCheck, Info, TriangleAlert } from "lucide-react";
import type * as React from "react";
import { cn } from "@/lib/utils";

const alertVariants = cva(
  "flex gap-3 border p-3 text-sm [&>svg]:mt-0.5 [&>svg]:size-4 [&>svg]:shrink-0",
  {
    variants: {
      variant: {
        info: "bg-card",
        success: "border-success bg-card text-success",
        warning: "border-warning bg-card",
        error: "border-destructive bg-card text-destructive",
      },
    },
    defaultVariants: { variant: "info" },
  },
);

const icons = { info: Info, success: CircleCheck, warning: TriangleAlert, error: CircleAlert };

export function Alert({
  className,
  variant,
  children,
  ...props
}: React.ComponentProps<"div"> & VariantProps<typeof alertVariants>) {
  const Icon = icons[variant ?? "info"];
  return (
    <div
      role={variant === "error" ? "alert" : "status"}
      className={cn(alertVariants({ variant }), className)}
      {...props}
    >
      <Icon aria-hidden />
      <div className="space-y-1">{children}</div>
    </div>
  );
}
