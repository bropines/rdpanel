import type { ColumnDef, RowSelectionState } from "@tanstack/react-table";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { beforeAll, describe, expect, it, vi } from "vitest";

import { DataTable } from "@/components/data-table";
import { TooltipProvider } from "@/components/ui/misc";
import { initI18n } from "@/i18n";

interface Row {
  id: number;
  name: string;
}

const columns: ColumnDef<Row, unknown>[] = [{ id: "name", header: "Name", cell: ({ row }) => row.original.name, meta: { label: "Name" } }];

function Harness(props: { data?: Row[]; total?: number; onSort?: (s: string | undefined) => void; onPage?: (p: number) => void; onBulk?: (ids: string[]) => void }) {
  const [sel, setSel] = useState<RowSelectionState>({});
  return (
    <TooltipProvider>
      <DataTable
        id="t"
        columns={columns}
        data={props.data}
        total={props.total}
        isLoading={false}
        error={null}
        page={1}
        pageSize={2}
        onPageChange={props.onPage ?? (() => undefined)}
        sort="name"
        onSortChange={props.onSort}
        getRowId={(r) => String(r.id)}
        selectable
        rowSelection={sel}
        onRowSelectionChange={setSel}
        bulkActions={(ids) => <button onClick={() => props.onBulk?.(ids)}>Bulk</button>}
      />
    </TooltipProvider>
  );
}

beforeAll(() => {
  localStorage.setItem("rdpanel-lang", "en");
  initI18n();
});

describe("DataTable", () => {
  it("renders the empty state", () => {
    render(<Harness data={[]} total={0} />);
    expect(screen.getByText("No data")).toBeInTheDocument();
    expect(screen.getByText("0–0 of 0")).toBeInTheDocument();
  });

  it("selects rows and passes the ids to bulk actions", async () => {
    const user = userEvent.setup();
    const onBulk = vi.fn();
    render(<Harness data={[{ id: 1, name: "a" }, { id: 2, name: "b" }]} total={5} onBulk={onBulk} />);
    const boxes = screen.getAllByRole("checkbox", { name: "Select row" });
    await user.click(boxes[1]!);
    expect(screen.getByText("1 selected")).toBeInTheDocument();
    await user.click(screen.getByRole("checkbox", { name: "Select all on page" }));
    expect(screen.getByText("2 selected")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Bulk" }));
    expect(onBulk).toHaveBeenCalledWith(["1", "2"]);
  });

  it("toggles server-side sort and paginates", async () => {
    const user = userEvent.setup();
    const onSort = vi.fn();
    const onPage = vi.fn();
    render(<Harness data={[{ id: 1, name: "a" }]} total={5} onSort={onSort} onPage={onPage} />);
    expect(screen.getByRole("columnheader", { name: /Name/ })).toHaveAttribute("aria-sort", "ascending");
    await user.click(screen.getByRole("button", { name: /Name/ }));
    expect(onSort).toHaveBeenCalledWith("-name");
    expect(screen.getByText("1 / 3")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Next page" }));
    expect(onPage).toHaveBeenCalledWith(2);
  });
});
