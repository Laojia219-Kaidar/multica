import type { Agent, AgentRuntime } from "@multica/core/types";
import type { AgentCostRow } from "./utils";

export type InventoryEvidence = "model_and_runtime" | "model_only" | "runtime_only";

export interface EmployeeUsageItem {
  id: string;
  name: string;
  model: string;
  observedTokens: number;
}

export interface ModelUsageItem {
  model: string;
  observedTokens: number;
  employeeCount: number;
}

export interface ModelPlanUsageRow {
  id: string;
  provider: string;
  plan: string;
  account: string;
  employees: EmployeeUsageItem[];
  models: ModelUsageItem[];
  observedTokens: number;
  evidence: InventoryEvidence;
}

export interface ModelProviderUsageGroup {
  provider: string;
  plans: ModelPlanUsageRow[];
  observedTokens: number;
  employeeCount: number;
}

export interface ModelQuotaUsageInventory {
  totalObservedTokens: number;
  employeeCount: number;
  planCount: number;
  providers: ModelProviderUsageGroup[];
  models: ModelUsageItem[];
}

interface ModelPlanClassification {
  key: string;
  provider: string;
  plan: string;
  account: string;
}

function classifyAgentPlan(
  agent: Pick<Agent, "model" | "runtime_id">,
  runtime: AgentRuntime | null,
): ModelPlanClassification {
  const model = agent.model?.trim() ?? "";
  const modelLower = model.toLowerCase();
  const runtimeName = runtime?.name.trim() ?? "";
  const runtimeLower = runtimeName.toLowerCase();
  const accountKey = runtime?.id ?? (modelLower || "unbound");

  const classified = (
    provider: string,
    plan: string,
    account = runtimeName || plan,
  ): ModelPlanClassification => ({
    key: `${provider}:${accountKey}`,
    provider,
    plan,
    account,
  });

  // The configured model is stronger than its qwen-compatible carrier here:
  // Atlas points at a local DGX checkpoint, not a Qwen cloud billing plan.
  if (modelLower.includes("qwen3.6-27b-nvfp4")) {
    return classified("本地模型 · DGX", "qwen3.6-27B NVFP4");
  }
  if (modelLower.startsWith("bailian-token-plan-personal/")) {
    return classified("阿里云百炼", "Token Plan Personal");
  }
  if (runtimeLower.includes("volcengine-agent")) {
    return classified("火山引擎 · Doubao", "Volcengine Agent Plan");
  }
  if (runtimeLower.includes("volcengine-coding")) {
    return classified("火山引擎 · Doubao", "Volcengine Coding Plan");
  }
  if (runtimeLower.includes("qwen-token")) {
    return classified("阿里云 · Qwen", "Qwen Token Plan");
  }
  if (runtimeLower.includes("qwen-coding")) {
    return classified("阿里云 · Qwen", "Qwen Coding Plan");
  }
  if (runtimeLower.includes("secure zhipu")) {
    const account = runtimeLower.match(/zhipu-(\d+)/)?.[1] ?? "1";
    return classified("智谱 · GLM", `GLM API 账户 #${account}`);
  }
  if (runtimeLower.includes("secure deepseek")) {
    return classified("DeepSeek", "DeepSeek V4 Flash API");
  }
  if (runtimeLower.includes("secure kimi")) {
    return classified("月之暗面 · Kimi", "Kimi K3 API");
  }
  if (runtimeLower.includes("secure mimo")) {
    return classified("小米 · MiMo", "MiMo V2.5 Pro API");
  }
  if (runtimeLower.includes("secure minimax")) {
    return classified("MiniMax", "MiniMax M3 API");
  }

  if (modelLower.startsWith("gpt-") || runtime?.provider === "codex") {
    return classified("OpenAI · Codex", "Codex Plan");
  }
  if (modelLower.startsWith("claude-") || runtime?.provider === "claude") {
    return classified("Anthropic · Claude", "Claude Plan");
  }
  if (modelLower.startsWith("glm-")) {
    return classified("智谱 · GLM", "GLM API");
  }
  if (modelLower === "k3" || runtime?.provider === "kimi") {
    return classified("月之暗面 · Kimi", "Kimi CLI");
  }
  if (modelLower.startsWith("qwen")) {
    return classified("阿里云 · Qwen", "Qwen Code");
  }
  if (modelLower.startsWith("deepseek")) {
    return classified("DeepSeek", "DeepSeek API");
  }
  if (modelLower.startsWith("minimax")) {
    return classified("MiniMax", model);
  }
  if (modelLower.startsWith("mimo")) {
    return classified("小米 · MiMo", model);
  }

  const runtimeProvider = runtime?.provider.toLowerCase() ?? "";
  const runtimeFallbacks: Record<string, [string, string]> = {
    coze: ["Coze", "Coze CLI"],
    hermes: ["Hermes", "Hermes Runtime"],
    openclaw: ["OpenClaw", "OpenClaw Runtime"],
    opencode: ["OpenCode", "OpenCode Runtime"],
    qoderclicn: ["Qoder", "Qoder CN"],
    qoder: ["Qoder", "Qoder"],
    qwen: ["阿里云 · Qwen", "Qwen Runtime"],
  };
  const [provider, plan] = runtimeFallbacks[runtimeProvider] ?? [
    runtime?.provider || "未识别提供商",
    runtimeName || model || "未绑定计费账户",
  ];
  return classified(provider, plan);
}

function aggregateModels(employees: readonly EmployeeUsageItem[]): ModelUsageItem[] {
  const models = new Map<string, ModelUsageItem>();
  for (const employee of employees) {
    const model = employee.model || "未标记模型";
    const current = models.get(model) ?? {
      model,
      observedTokens: 0,
      employeeCount: 0,
    };
    current.observedTokens += employee.observedTokens;
    current.employeeCount += 1;
    models.set(model, current);
  }
  return Array.from(models.values()).toSorted(
    (a, b) => b.observedTokens - a.observedTokens || a.model.localeCompare(b.model),
  );
}

export function buildModelQuotaUsageInventory(
  agents: readonly Agent[],
  runtimes: readonly AgentRuntime[],
  tokenRows: readonly AgentCostRow[],
): ModelQuotaUsageInventory {
  const runtimeById = new Map(runtimes.map((runtime) => [runtime.id, runtime]));
  const tokensByAgent = new Map(tokenRows.map((row) => [row.agentId, row.tokens]));
  const groupedPlans = new Map<
    string,
    Omit<ModelPlanUsageRow, "models" | "evidence"> & {
      evidenceParts: Set<InventoryEvidence>;
    }
  >();

  for (const agent of agents) {
    const runtime = agent.runtime_id
      ? (runtimeById.get(agent.runtime_id) ?? null)
      : null;
    const classification = classifyAgentPlan(agent, runtime);
    const evidence: InventoryEvidence = agent.model?.trim()
      ? runtime
        ? "model_and_runtime"
        : "model_only"
      : "runtime_only";
    const current = groupedPlans.get(classification.key) ?? {
      id: classification.key,
      provider: classification.provider,
      plan: classification.plan,
      account: classification.account,
      employees: [],
      observedTokens: 0,
      evidenceParts: new Set<InventoryEvidence>(),
    };
    const observedTokens = tokensByAgent.get(agent.id) ?? 0;
    current.employees.push({
      id: agent.id,
      name: agent.name,
      model: agent.model?.trim() ?? "",
      observedTokens,
    });
    current.observedTokens += observedTokens;
    current.evidenceParts.add(evidence);
    groupedPlans.set(classification.key, current);
  }

  const plans: ModelPlanUsageRow[] = Array.from(groupedPlans.values()).map(
    ({ evidenceParts, ...row }): ModelPlanUsageRow => ({
      ...row,
      employees: row.employees.toSorted(
        (a, b) => b.observedTokens - a.observedTokens || a.name.localeCompare(b.name),
      ),
      models: aggregateModels(row.employees),
      evidence: evidenceParts.has("model_and_runtime")
        ? "model_and_runtime"
        : evidenceParts.has("model_only")
          ? "model_only"
          : "runtime_only",
    }),
  );

  const providerMap = new Map<string, ModelProviderUsageGroup>();
  for (const plan of plans) {
    const provider = providerMap.get(plan.provider) ?? {
      provider: plan.provider,
      plans: [],
      observedTokens: 0,
      employeeCount: 0,
    };
    provider.plans.push(plan);
    provider.observedTokens += plan.observedTokens;
    provider.employeeCount += plan.employees.length;
    providerMap.set(plan.provider, provider);
  }

  const providers = Array.from(providerMap.values())
    .map((provider) => ({
      ...provider,
      plans: provider.plans.toSorted(
        (a, b) => b.observedTokens - a.observedTokens || a.plan.localeCompare(b.plan),
      ),
    }))
    .toSorted(
      (a, b) =>
        b.observedTokens - a.observedTokens || a.provider.localeCompare(b.provider),
    );
  const allEmployees = plans.flatMap((plan) => plan.employees);

  return {
    totalObservedTokens: allEmployees.reduce(
      (total, employee) => total + employee.observedTokens,
      0,
    ),
    employeeCount: agents.length,
    planCount: plans.length,
    providers,
    models: aggregateModels(allEmployees),
  };
}
