import { resolveServerConfig } from "@/lib/server/config";
import { AdminNotConfigured } from "@/components/admin-not-configured";
import { TenantCreateForm } from "@/components/tenant-create-form";

export default function Page() {
  if (!resolveServerConfig(process.env as Record<string, string | undefined>).adminKey) return <AdminNotConfigured />;
  return <TenantCreateForm />;
}
