import { Instrument_Serif } from "next/font/google";
import { LocaleProvider } from "@/features/landing/i18n";
import { getRequestLocale } from "@/lib/request-locale";
import {
  HIVE_CREW_LOCAL_ENDPOINTS,
  HIVE_CREW_PRODUCT,
  HIVE_CREW_PRODUCT_NAME,
} from "@multica/core/product";

// Instrument Serif is the landing display face and is Latin-only. The full
// `--font-serif` stack (Instrument Serif + the per-locale CJK serif tail) is
// composed in static CSS in app/custom.css, not here — same reasoning as
// `--font-sans` in app/globals.css: the CJK tail must be overridable per
// `<html lang>`, and a hashed next/font family can only be referenced from CSS
// through its variable.
const instrumentSerif = Instrument_Serif({
  subsets: ["latin"],
  weight: "400",
  variable: "--font-instrument-serif",
});

const publicAppUrl =
  process.env.HIVECREW_PUBLIC_APP_URL ?? HIVE_CREW_LOCAL_ENDPOINTS.appUrl;

const jsonLd = {
  "@context": "https://schema.org",
  "@graph": [
    {
      "@type": "Organization",
      name: HIVE_CREW_PRODUCT.companyName,
      url: publicAppUrl,
    },
    {
      "@type": "SoftwareApplication",
      name: HIVE_CREW_PRODUCT_NAME,
      applicationCategory: "ProjectManagement",
      operatingSystem: "Web",
      description: HIVE_CREW_PRODUCT.tagline,
      offers: {
        "@type": "Offer",
        price: "0",
        priceCurrency: "USD",
      },
    },
  ],
};

export default async function LandingLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  const initialLocale = await getRequestLocale();

  return (
    <>
      <script
        type="application/ld+json"
        dangerouslySetInnerHTML={{ __html: JSON.stringify(jsonLd) }}
      />
      <div className={`${instrumentSerif.variable} landing-light h-full overflow-x-hidden overflow-y-auto bg-white`}>
        <LocaleProvider initialLocale={initialLocale}>{children}</LocaleProvider>
      </div>
    </>
  );
}
