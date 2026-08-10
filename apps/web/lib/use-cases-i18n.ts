import type { SupportedLocale } from "@multica/core/i18n";
export { docsHrefForLocale } from "@/lib/docs-href";
import { getRequestLocale } from "@/lib/request-locale";

export const getUseCaseLocale = getRequestLocale;

type UseCaseText = {
  indexTitle: string;
  indexSubtitle: string;
  indexMetadataTitle: string;
  indexMetadataDescription: string;
  cardReadMore: string;
  tableOfContents: string;
  inheritedBaselineNotice: string;
};

export const useCaseText: Record<SupportedLocale, UseCaseText> = {
  en: {
    indexTitle: "Use cases",
    indexSubtitle:
      "Study inherited reference cases that informed HiveCrew's human and digital-employee workflows.",
    indexMetadataTitle: "Use cases",
    indexMetadataDescription:
      "Inherited baseline examples retained for design provenance; they are not HiveCrew customer claims.",
    cardReadMore: "Read →",
    tableOfContents: "On this page",
    inheritedBaselineNotice:
      "Inherited reference: this article describes the Multica baseline and is retained for provenance. It is not a HiveCrew customer case or release claim.",
  },
  "zh-Hans": {
    indexTitle: "案例",
    indexSubtitle: "阅读影响 HiveCrew 人类员工与数字员工协同设计的继承参考案例。",
    indexMetadataTitle: "案例",
    indexMetadataDescription:
      "以下为保留设计来源的继承基线案例，不代表 HiveCrew 客户案例。",
    cardReadMore: "阅读 →",
    tableOfContents: "目录",
    inheritedBaselineNotice:
      "继承参考资料：本文描述的是 Multica 基线，仅为保留设计来源，不代表 HiveCrew 的客户案例或版本成果。",
  },
  ko: {
    indexTitle: "사용 사례",
    indexSubtitle:
      "HiveCrew의 사람과 디지털 직원 협업 설계에 참고한 상속 사례를 확인하세요.",
    indexMetadataTitle: "사용 사례",
    indexMetadataDescription:
      "설계 출처를 보존하기 위한 상속 베이스라인 사례이며 HiveCrew 고객 사례가 아닙니다.",
    cardReadMore: "읽기 →",
    tableOfContents: "이 페이지에서",
    inheritedBaselineNotice:
      "상속 참고 자료: 이 글은 Multica 베이스라인을 설명하며 설계 출처 보존을 위해 남겨 두었습니다. HiveCrew 고객 사례나 릴리스 주장이 아닙니다.",
  },
  ja: {
    indexTitle: "ユースケース",
    indexSubtitle:
      "HiveCrew の人とデジタル社員の協働設計に影響した継承参考事例をご覧ください。",
    indexMetadataTitle: "ユースケース",
    indexMetadataDescription:
      "設計の出典を保持するための継承ベースライン事例であり、HiveCrew の顧客事例ではありません。",
    cardReadMore: "続きを読む →",
    tableOfContents: "このページの内容",
    inheritedBaselineNotice:
      "継承参考資料: この記事は Multica ベースラインを説明し、設計の出典を保持するために残しています。HiveCrew の顧客事例やリリース実績ではありません。",
  },
};
