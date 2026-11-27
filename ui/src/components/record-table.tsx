import type { ComponentProps, ReactNode } from "react";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { cn } from "@/lib/utils";

export function RecordTable({
  id,
  columns,
  children,
}: {
  id: string;
  columns: string[];
  children: ReactNode;
}) {
  return (
    <Table
      role="table"
      aria-labelledby={id}
      className="directory-table block min-w-0 border-collapse lg:table"
    >
      <TableHeader className="sr-only text-left text-sm lg:not-sr-only lg:table-header-group">
        <TableRow>
          {columns.map((column) => (
            <TableHead
              scope="col"
              className={cn(
                "pb-2 whitespace-normal text-muted-foreground",
                column === "Select" && "selection-cell",
              )}
              key={column}
            >
              {column}
            </TableHead>
          ))}
        </TableRow>
      </TableHeader>
      <TableBody className="block lg:table-row-group">{children}</TableBody>
    </Table>
  );
}

export function RecordRow({ className, ...props }: ComponentProps<"tr">) {
  return (
    <TableRow
      className={cn("grid min-w-0 gap-2 py-4 lg:table-row", className)}
      {...props}
    />
  );
}

export function NameCell({ children }: { children: ReactNode }) {
  return (
    <TableHead
      scope="row"
      className="entry-name-cell h-auto min-w-0 p-0 text-left font-medium break-all whitespace-normal lg:px-2 lg:py-3"
    >
      {children}
    </TableHead>
  );
}

export function Cell({
  label,
  children,
}: {
  label?: string;
  children: ReactNode;
}) {
  return (
    <TableCell className="min-w-0 p-0 text-sm break-words whitespace-normal lg:px-2 lg:py-3">
      {label && (
        <span
          aria-hidden="true"
          className="mobile-label mr-2 text-muted-foreground lg:hidden"
        >
          {label}
        </span>
      )}
      {children}
    </TableCell>
  );
}

export function PickCell({ children }: { children: ReactNode }) {
  return (
    <TableCell className="selection-cell w-fit p-0 whitespace-normal lg:w-12 lg:px-2 lg:py-3">
      {children}
    </TableCell>
  );
}
