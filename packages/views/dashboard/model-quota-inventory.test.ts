import { describe, expect, it } from "vitest";
import type {
  Agent,
  AgentRuntime,
  DashboardUsageByAgent,
} from "@multica/core/types";
import { buildModelQuotaUsageInventory } from "./model-quota-inventory";

function agent(
  id: string,
  name: string,
  model: string,
  runtimeId: string,
): Agent {
  return { id, name, model, runtime_id: runtimeId } as Agent;
}

function runtime(
  id: string,
  name: string,
  provider = "qwen",
  profileId?: string,
): AgentRuntime {
  return { id, name, provider, profile_id: profileId } as AgentRuntime;
}

function usage(
  agentId: string,
  model: string,
  tokens: number,
  provider = "qwen",
): DashboardUsageByAgent {
  return {
    agent_id: agentId,
    provider,
    model,
    input_tokens: tokens,
    output_tokens: 0,
    cache_read_tokens: 0,
    cache_write_tokens: 0,
    task_count: 1,
  };
}

describe("buildModelQuotaUsageInventory", () => {
  it("reconciles employee, account, provider, model, and company totals", () => {
    const inventory = buildModelQuotaUsageInventory(
      [
        agent("raven", "Raven", "qwen3.7-plus", "qwen-coding"),
        agent("pixel", "Pixel", "qwen3.7-plus", "qwen-coding"),
        agent("atlas", "Atlas", "qwen3.6-27b-nvfp4", "qwen-coding"),
        agent("kai", "Kai", "glm-5.2", "glm-1"),
        agent("aria", "Aria", "glm-5.2", "glm-2"),
      ],
      [
        runtime("qwen-coding", "HiveCosm Secure qwen-coding"),
        runtime("glm-1", "HiveCosm Secure zhipu"),
        runtime("glm-2", "HiveCosm Secure zhipu-2"),
      ],
      [
        usage("raven", "qwen3.7-plus", 100),
        usage("pixel", "qwen3.7-plus", 250),
        usage("atlas", "qwen3.6-27b-nvfp4", 200),
        usage("kai", "glm-5.2", 300),
        usage("aria", "glm-5.2", 150),
      ],
    );

    expect(inventory).toMatchObject({
      totalObservedTokens: 1_000,
      employeeCount: 5,
      planCount: 4,
    });
    expect(
      inventory.providers
        .flatMap((provider) => provider.plans)
        .flatMap((plan) => plan.employees)
        .map((employee) => employee.id)
        .toSorted(),
    ).toEqual(["aria", "atlas", "kai", "pixel", "raven"]);

    const qwen = inventory.providers.find(
      (provider) => provider.provider === "阿里云 · Qwen",
    );
    expect(qwen).toMatchObject({ observedTokens: 350, employeeCount: 2 });
    expect(qwen?.plans[0]).toMatchObject({
      observedTokens: 350,
      employees: [
        { id: "pixel", observedTokens: 250 },
        { id: "raven", observedTokens: 100 },
      ],
    });

    expect(inventory.models).toEqual(
      expect.arrayContaining([
        { model: "glm-5.2", observedTokens: 450, employeeCount: 2 },
        { model: "qwen3.7-plus", observedTokens: 350, employeeCount: 2 },
        {
          model: "qwen3.6-27b-nvfp4",
          observedTokens: 200,
          employeeCount: 1,
        },
      ]),
    );
  });

  it("keeps separate API-key runtimes as separate billing accounts", () => {
    const inventory = buildModelQuotaUsageInventory(
      [
        agent("kai", "Kai", "glm-5.2", "glm-1"),
        agent("aria", "Aria", "glm-5.2", "glm-2"),
      ],
      [
        runtime("glm-1", "HiveCosm Secure zhipu"),
        runtime("glm-2", "HiveCosm Secure zhipu-2"),
      ],
      [],
    );

    const glm = inventory.providers.find(
      (provider) => provider.provider === "智谱 · GLM",
    );
    expect(glm?.plans.map((plan) => plan.plan).toSorted()).toEqual([
      "GLM API 账户 #1",
      "GLM API 账户 #2",
    ]);
  });

  it("groups Bailian runtimes by billing profile and keeps receipt models honest", () => {
    const model = "bailian-token-plan-personal/deepseek-v4-flash-0731";
    const inventory = buildModelQuotaUsageInventory(
      [
        agent("atelier", "Atelier", model, "runtime-a"),
        agent("finn", "Finn", model, "runtime-b"),
      ],
      [
        runtime("runtime-a", "HiveCosm Secure qwen-token A", "qwen", "profile-1"),
        runtime("runtime-b", "HiveCosm Secure qwen-token B", "qwen", "profile-1"),
      ],
      [
        usage("atelier", model, 100, "opencode"),
        usage("finn", model, 200, "opencode"),
        usage("finn", "qwen3.8-max", 50, "qwen"),
      ],
    );

    const bailian = inventory.providers.find(
      (provider) => provider.provider === "阿里云百炼",
    );
    expect(bailian).toMatchObject({ employeeCount: 2, observedTokens: 300 });
    expect(bailian?.plans).toHaveLength(1);
    expect(bailian?.plans[0]).toMatchObject({
      account: "bailian-token-plan-personal",
      observedTokens: 300,
      quota: {
        windowDays: 7,
        totalTokens: 415_592_437,
        usedTokens: 415_592_437,
        remainingTokens: 0,
        usedRatio: 1,
        resetAt: null,
        evidence: "owner_confirmed_zero_based_full_window_total",
      },
    });
    expect(bailian?.plans[0]?.employees.map((employee) => employee.id).toSorted()).toEqual([
      "atelier",
      "finn",
    ]);

    const qwen = inventory.providers.find(
      (provider) => provider.provider === "阿里云 · Qwen",
    );
    expect(qwen?.observedTokens).toBe(50);
    expect(inventory.totalObservedTokens).toBe(350);
  });
});
