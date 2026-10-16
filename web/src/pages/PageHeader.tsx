import type { ReactNode } from "react";

/** A page's title and one-line description, with optional buttons on the right. */
export function PageHeader({
  title,
  description,
  actions,
}: {
  title: string;
  description?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <div className="mb-6 flex flex-wrap items-end gap-4">
      <div className="min-w-0 flex-[1_1_320px]">
        <h1 className="text-2xl/8 font-semibold">{title}</h1>
        {description && (
          <p className="mt-1 max-w-[44rem] text-sm text-muted-foreground">{description}</p>
        )}
      </div>
      {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
    </div>
  );
}
