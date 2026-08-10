import type { Metadata } from "next";
import { AboutPageClient } from "@/features/landing/components/about-page-client";

export const metadata: Metadata = {
  title: "About",
  description:
    "Learn about HiveCrew, HiveCosm's independent operating workspace for human and digital-employee teams.",
  openGraph: {
    title: "About HiveCrew",
    description:
      "Why HiveCosm is building HiveCrew for human and digital-employee teams.",
    url: "/about",
  },
  alternates: {
    canonical: "/about",
  },
};

export default function AboutPage() {
  return <AboutPageClient />;
}
