import { redirect } from "next/navigation";

/**
 * Root landing page.
 *
 * The page is dynamic and redirects to the dashboard path. The locale is never
 * a URL segment, so the redirect target carries no locale prefix.
 */
export const dynamic = "force-dynamic";

export default function RootPage() {
  redirect("/dashboard");
}