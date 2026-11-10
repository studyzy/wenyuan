// @vitest-environment jsdom
import { render, screen, fireEvent } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ProjectRows } from "@/components/team-project-rows";

vi.mock("next-intl", () => ({
  useTranslations: () => (key: string) => key,
}));

const project = {
  project_id: "web",
  name: "Web",
  bank_id: "acme::web",
  member_count: 1,
} as never;

describe("ProjectRows rebuild control", () => {
  it("renders the rebuild button beside the delete control", () => {
    const onRebuild = vi.fn();
    render(
      <table><tbody>
        <ProjectRows
          project={project} expanded={false} members={[]} candidates={[]}
          candidate="" onCandidate={() => {}} onToggle={() => {}}
          onAddMember={() => {}} onRemoveMember={() => {}}
          onPurgeMember={() => {}} onRebuild={onRebuild} onDelete={() => {}}
        />
      </tbody></table>,
    );
    const button = screen.getByText("rebuildBank");
    expect(button).toBeDefined();
    fireEvent.click(button);
    expect(onRebuild).toHaveBeenCalledOnce();
  });
});
