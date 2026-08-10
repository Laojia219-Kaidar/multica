import type { Metadata } from "next";
import { HiveCrewLanding } from "@/features/landing/components/multica-landing";

export const metadata: Metadata = {
  title: "Homepage",
  description:
    "HiveCrew — HiveCosm's digital employee collaboration and execution workspace.",
  openGraph: {
    title: "HiveCrew — Digital Employee Collaboration and Execution",
    description:
      "Manage your human + agent workforce in one place.",
    url: "/homepage",
  },
  alternates: {
    canonical: "/homepage",
  },
};

export default function HomepagePage() {
  return <HiveCrewLanding />;
}
