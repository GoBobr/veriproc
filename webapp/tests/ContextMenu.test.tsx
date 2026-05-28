import { describe, it, expect } from "vitest";
import { render, screen, fireEvent } from "@testing-library/preact";
import { ContextMenu } from "../src/components/ContextMenu";

describe("ContextMenu", () => {
  it("invokes the handler and closes", () => {
    let closed = false;
    let clicked = false;
    render(
      <ContextMenu
        x={10}
        y={10}
        items={[{ label: "Hi", onClick: () => (clicked = true) }]}
        onClose={() => (closed = true)}
      />
    );
    fireEvent.click(screen.getByText("Hi"));
    expect(clicked).toBe(true);
    expect(closed).toBe(true);
  });

  it("respects the disabled flag", () => {
    let clicked = false;
    render(
      <ContextMenu
        x={0}
        y={0}
        items={[{ label: "No", onClick: () => (clicked = true), disabled: true }]}
        onClose={() => {}}
      />
    );
    fireEvent.click(screen.getByText("No"));
    expect(clicked).toBe(false);
  });
});
