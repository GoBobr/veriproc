import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/preact";
import { SubmitDialog } from "../src/components/SubmitDialog";

describe("SubmitDialog", () => {
  it("rejects when end <= start", async () => {
    const onSubmit = vi.fn();
    const onClose = vi.fn();
    render(<SubmitDialog stationID="st" onSubmit={onSubmit} onClose={onClose} />);
    // set start ahead of end
    const start = screen.getByLabelText(/Start/i) as HTMLInputElement;
    const end = screen.getByLabelText(/End/i) as HTMLInputElement;
    fireEvent.input(start, { target: { value: "2024-01-02T01:00" } });
    fireEvent.input(end, { target: { value: "2024-01-02T01:00" } });
    fireEvent.submit(start.closest("form")!);
    // give the async submit a tick
    await Promise.resolve();
    expect(onSubmit).not.toHaveBeenCalled();
    expect(await screen.findByText(/end must be after start/)).toBeInTheDocument();
  });

  it("submits an ISO body when valid", async () => {
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    const onClose = vi.fn();
    render(<SubmitDialog stationID="st" onSubmit={onSubmit} onClose={onClose} />);
    const start = screen.getByLabelText(/Start/i) as HTMLInputElement;
    const end = screen.getByLabelText(/End/i) as HTMLInputElement;
    fireEvent.input(start, { target: { value: "2024-01-02T00:00" } });
    fireEvent.input(end, { target: { value: "2024-01-02T01:00" } });
    fireEvent.submit(start.closest("form")!);
    await Promise.resolve();
    await Promise.resolve();
    expect(onSubmit).toHaveBeenCalledTimes(1);
    const arg = onSubmit.mock.calls[0][0];
    expect(arg.start).toMatch(/T/);
    expect(arg.end).toMatch(/T/);
    expect(new Date(arg.end).getTime()).toBeGreaterThan(new Date(arg.start).getTime());
  });
});
