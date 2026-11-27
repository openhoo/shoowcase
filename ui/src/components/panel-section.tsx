import type { ComponentProps } from "react";
import { Card, CardContent } from "@/components/ui/card";

export function PanelSection({
  children,
  className,
  ...section
}: ComponentProps<"section">) {
  return (
    <section {...section} className={className}>
      <Card className="min-w-0 gap-0 py-0">
        <CardContent className="min-w-0 space-y-4 p-4 sm:p-6">
          {children}
        </CardContent>
      </Card>
    </section>
  );
}
