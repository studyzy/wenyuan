import { resolveServerConfig } from "@/lib/server/config";
import { AdminNotConfigured } from "@/components/admin-not-configured";
import { OverviewView } from "@/components/overview-view";

export default function Page() {
  if (!resolveServerConfig(process.env as Record<string, string | undefined>).adminKey) return <AdminNotConfigured />;
  return <OverviewView />;
}
