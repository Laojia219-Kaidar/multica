"use client";

import Image from "next/image";
import Link from "next/link";
import { ArrowRight, Download } from "lucide-react";
import { useAuthStore } from "@multica/core/auth";
import { useLocale } from "../i18n";
import { useDashboardCtaHref } from "../utils/use-dashboard-cta";
import {
  ClaudeCodeLogo,
  CodexLogo,
  OpenClawLogo,
  OpenCodeLogo,
  heroButtonClassName,
} from "./shared";

export function LandingHero() {
  const { t } = useLocale();
  const user = useAuthStore((s) => s.user);
  const ctaHref = useDashboardCtaHref();

  return (
    <div className="relative min-h-full overflow-hidden bg-[#05070b] text-white">
      <LandingBackdrop />

      <main className="relative z-10">
        <section
          id="product"
          className="mx-auto max-w-[1320px] px-4 pb-16 pt-28 sm:px-6 sm:pt-32 lg:px-8 lg:pb-24 lg:pt-36"
        >
          <div className="mx-auto max-w-[1120px] text-center">
            <h1 className="landing-serif text-[3.65rem] leading-[0.93] tracking-[-0.038em] text-white drop-shadow-[0_10px_34px_rgba(0,0,0,0.32)] sm:text-[4.85rem] lg:text-[6.4rem]">
              {t.hero.headlineLine1}
              <br />
              {t.hero.headlineLine2}
            </h1>

            <p className="mx-auto mt-7 max-w-[820px] text-[15px] leading-7 text-white/84 sm:text-[17px]">
              {t.hero.subheading}
            </p>

            <div className="mt-8 flex flex-wrap items-center justify-center gap-3">
              <Link href={ctaHref} className={heroButtonClassName("solid")}>
                {user ? t.header.dashboard : t.hero.cta}
              </Link>
              <Link
                href="/download"
                className={heroButtonClassName("ghost")}
              >
                <Download className="size-4" aria-hidden />
                {t.hero.downloadDesktop}
              </Link>
              <Link
                href="/contact-sales"
                className="group inline-flex items-center justify-center gap-1.5 rounded-[12px] px-3 py-3 text-[14px] font-semibold text-white/80 transition-colors hover:text-white"
              >
                {t.hero.talkToSales}
                <ArrowRight
                  className="size-4 transition-transform group-hover:translate-x-0.5"
                  aria-hidden
                />
              </Link>
            </div>
          </div>

          <div className="mt-10 flex flex-wrap items-center justify-center gap-x-6 gap-y-3">
            <span className="text-[15px] text-white/50">
              {t.hero.worksWith}
            </span>
            <div className="flex flex-wrap items-center justify-center gap-x-5 gap-y-3">
              <div className="flex items-center gap-2.5 text-white/80">
                <ClaudeCodeLogo className="size-5" />
                <span className="text-[15px] font-medium">Claude Code</span>
              </div>
              <div className="flex items-center gap-2.5 text-white/80">
                <CodexLogo className="size-5" />
                <span className="text-[15px] font-medium">Codex</span>
              </div>
              <div className="flex items-center gap-2.5 text-white/80">
                <OpenClawLogo className="size-5" />
                <span className="text-[15px] font-medium">OpenClaw</span>
              </div>
              <div className="flex items-center gap-2.5 text-white/80">
                <OpenCodeLogo className="size-5" />
                <span className="text-[15px] font-medium">OpenCode</span>
              </div>
            </div>
          </div>

          <div id="preview" className="mt-10 sm:mt-12">
            <ProductImage alt={t.hero.imageAlt} />
          </div>
        </section>
      </main>
    </div>
  );
}

function LandingBackdrop() {
  return (
    <div className="pointer-events-none absolute inset-0">
      <Image
        src="/images/landing-bg.jpg"
        alt=""
        fill
        className="object-cover object-center"
      />
    </div>
  );
}

function ProductImage({ alt }: { alt: string }) {
  return (
    <div
      role="img"
      aria-label={alt}
      className="overflow-hidden rounded-[18px] border border-white/15 bg-[#0a0d14]/95 text-left shadow-2xl shadow-black/35"
    >
      <div className="flex h-10 items-center gap-2 border-b border-white/10 px-4 text-[11px] text-white/45">
        <span className="size-2.5 rounded-full bg-[#ff6b62]" />
        <span className="size-2.5 rounded-full bg-[#f4c44e]" />
        <span className="size-2.5 rounded-full bg-[#47c96f]" />
        <span className="ml-3 font-medium text-white/70">HiveCrew Command Center</span>
      </div>
      <div className="grid min-h-[310px] grid-cols-[150px_minmax(0,1fr)] sm:min-h-[390px] sm:grid-cols-[210px_minmax(0,1fr)_260px]">
        <aside className="border-r border-white/10 p-4 sm:p-5">
          <div className="mb-7 flex items-center gap-2 font-semibold text-white">
            <span className="text-xl">＊</span>
            <span>HiveCrew</span>
          </div>
          <div className="space-y-1.5 text-[12px] text-white/52 sm:text-[13px]">
            {['Inbox', 'Conversations', 'My work', 'Projects', 'Employees'].map((item, index) => (
              <div
                key={item}
                className={index === 2 ? 'rounded-lg bg-white/10 px-3 py-2 text-white' : 'px-3 py-2'}
              >
                {item}
              </div>
            ))}
          </div>
        </aside>
        <section className="min-w-0 p-5 sm:p-7">
          <div className="text-[11px] font-medium uppercase tracking-[0.15em] text-white/35">Owner workspace</div>
          <div className="mt-2 text-xl font-semibold text-white sm:text-2xl">Company command center</div>
          <div className="mt-6 grid grid-cols-2 gap-3 lg:grid-cols-4">
            {[
              ['Digital employees', '12'],
              ['Working now', '7'],
              ['Active projects', '18'],
              ['Needs review', '3'],
            ].map(([label, value]) => (
              <div key={label} className="rounded-xl border border-white/10 bg-white/[0.035] p-3">
                <div className="text-[10px] text-white/38 sm:text-[11px]">{label}</div>
                <div className="mt-1 text-xl font-semibold text-white">{value}</div>
              </div>
            ))}
          </div>
          <div className="mt-5 rounded-xl border border-white/10 bg-white/[0.025]">
            <div className="border-b border-white/10 px-4 py-3 text-[12px] font-medium text-white/80">Current work</div>
            {[
              ['Coco · CEO', 'Coordinating HiveCrew B1', 'In progress'],
              ['Atlas · Full-stack Engineer', 'Employee registry workspace', 'Working'],
              ['Kimi · Architecture Reviewer', 'Runtime model review', 'Ready'],
            ].map(([person, work, status]) => (
              <div key={person} className="grid grid-cols-[minmax(0,1fr)_auto] gap-4 border-b border-white/[0.06] px-4 py-3 last:border-0">
                <div className="min-w-0">
                  <div className="truncate text-[11px] font-medium text-white/78 sm:text-[12px]">{person}</div>
                  <div className="truncate text-[10px] text-white/35 sm:text-[11px]">{work}</div>
                </div>
                <div className="self-center rounded-full bg-[#72d69a]/10 px-2 py-1 text-[9px] text-[#72d69a] sm:text-[10px]">{status}</div>
              </div>
            ))}
          </div>
        </section>
        <aside className="hidden border-l border-white/10 p-5 sm:block">
          <div className="text-[11px] font-medium uppercase tracking-[0.15em] text-white/35">Conversation</div>
          <div className="mt-5 rounded-xl bg-white/[0.045] p-3 text-[11px] leading-relaxed text-white/55">
            Assign the next milestone to the engineering team and report blockers here.
          </div>
          <div className="mt-3 rounded-xl border border-[#6ea8ff]/25 bg-[#6ea8ff]/8 p-3 text-[11px] leading-relaxed text-white/68">
            Coco has created three work items and linked them to the HiveCrew project.
          </div>
          <div className="mt-20 rounded-xl border border-white/10 px-3 py-3 text-[11px] text-white/30">Message Coco…</div>
        </aside>
      </div>
    </div>
  );
}
