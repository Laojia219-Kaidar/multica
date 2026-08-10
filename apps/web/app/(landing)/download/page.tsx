import type { Metadata } from "next";
import { fetchLatestRelease } from "@/features/landing/utils/github-release";
import { DownloadClient } from "./download-client";

// Vercel ISR: the server fetch inside fetchLatestRelease carries
// `next: { revalidate: 300 }`, which makes GitHub API cost at most
// one request per region per 5 minutes. Page-level revalidate mirrors
// that window so the first paint also refreshes every 5 minutes.
export const revalidate = 300;

export const metadata: Metadata = {
  title: "Download HiveCrew",
  description:
    "Download HiveCrew for macOS, Windows, or Linux — or install the compatible CLI for servers and remote development hosts.",
  openGraph: {
    title: "Download HiveCrew",
    description:
      "Get the HiveCrew desktop app with a bundled daemon, or install the compatible CLI for servers and remote development hosts.",
    url: "/download",
  },
  alternates: {
    canonical: "/download",
  },
};

export default async function DownloadPage() {
  const release = await fetchLatestRelease();
  return <DownloadClient release={release} />;
}
