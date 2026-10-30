import { resolveServerConfig } from "@/lib/server/config";
import { AdminNotConfigured } from "@/components/admin-not-configured";
import { TeamDetailView } from "@/components/team-detail-view";

export default async function Page({
  params,
}: {
  params: Promise<{ teamId: string }>;
}) {
  if (!resolveServerConfig(process.env as Record<string, string | undefined>).adminKey) return <AdminNotConfigured />;
  const { teamId } = await params;
  return <TeamDetailView teamId={teamId} />;
}
