import type { Metadata } from "next";
import { ChangelogPageClient } from "@/features/landing/components/changelog-page-client";

export const metadata: Metadata = {
  title: "Inherited Baseline Changelog",
  description:
    "Historical releases from the inherited baseline. HiveCrew releases begin after the independent fork.",
  openGraph: {
    title: "Inherited Baseline Changelog | HiveCrew",
    description: "Historical inherited releases retained for provenance.",
    url: "/changelog",
  },
  alternates: {
    canonical: "/changelog",
  },
};

export default function ChangelogPage() {
  return <ChangelogPageClient />;
}
