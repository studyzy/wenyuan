import { resolveServerConfig } from "@/lib/server/config";
import { AdminNotConfigured } from "@/components/admin-not-configured";
import { TenantDetailView } from "@/components/tenant-detail-view";

export default async function Page({
  params,
}: {
  params: Promise<{ tenantId: string }>;
}) {
  if (!resolveServerConfig(process.env as Record<string, string | undefined>).adminKey) return <AdminNotConfigured />;
  const { tenantId } = await params;
  return <TenantDetailView tenantId={tenantId} />;
}
