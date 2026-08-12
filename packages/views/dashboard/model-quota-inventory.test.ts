import { describe, expect, it } from "vitest";
import type { Agent, AgentRuntime } from "@multica/core/types";
import { buildModelQuotaUsageInventory } from "./model-quota-inventory";

function agent(
  id: string,
  name: string,
  model: string,
  runtimeId: string,
): Agent {
  return { id, name, model, runtime_id: runtimeId } as Agent;
}

function runtime(id: string, name: string, provider = "qwen"): AgentRuntime {
  return { id, name, provider } as AgentRuntime;
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
        { agentId: "raven", tokens: 100, cost: 0, taskCount: 1 },
        { agentId: "pixel", tokens: 250, cost: 0, taskCount: 1 },
        { agentId: "atlas", tokens: 200, cost: 0, taskCount: 1 },
        { agentId: "kai", tokens: 300, cost: 0, taskCount: 1 },
        { agentId: "aria", tokens: 150, cost: 0, taskCount: 1 },
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
});
