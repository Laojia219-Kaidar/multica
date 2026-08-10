import type { Metadata } from "next";
import { ContactSalesPageClient } from "@/features/landing/components/contact-sales-page-client";

export const metadata: Metadata = {
  title: "Contact Sales",
  description:
    "Talk to the HiveCrew team about human and digital-employee workflows for your company.",
  openGraph: {
    title: "Contact Sales — HiveCrew",
    description:
      "Tell us about your team. We’ll respond within three business days.",
    url: "/contact-sales",
  },
  alternates: {
    canonical: "/contact-sales",
  },
};

export default function ContactSalesPage() {
  return <ContactSalesPageClient />;
}
