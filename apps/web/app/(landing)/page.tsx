import type { Metadata } from "next";
import { HiveCrewLanding } from "@/features/landing/components/multica-landing";
import { RedirectIfAuthenticated } from "@/features/landing/components/redirect-if-authenticated";

export const metadata: Metadata = {
  title: {
    absolute: "HiveCrew — Digital Employee Collaboration and Execution",
  },
  description:
    "HiveCosm's digital employee collaboration and execution workspace.",
  openGraph: {
    title: "HiveCrew — Digital Employee Collaboration and Execution",
    description:
      "Manage your human + agent workforce in one place.",
    url: "/",
  },
  alternates: {
    canonical: "/",
  },
};

export default function LandingPage() {
  return (
    <>
      <RedirectIfAuthenticated />
      <HiveCrewLanding />
    </>
  );
}
