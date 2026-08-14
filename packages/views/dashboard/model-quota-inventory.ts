import type {
  Agent,
  AgentRuntime,
  DashboardUsageByAgent,
} from "@multica/core/types";

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

export interface ModelPlanQuotaSnapshot {
  windowDays: number;
  totalTokens: number;
  usedTokens: number;
  remainingTokens: number;
  usedRatio: number;
  resetAt: string | null;
  observedAt: string;
  evidence: "owner_confirmed_exhaustion_and_workspace_observation";
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
  quota: ModelPlanQuotaSnapshot | null;
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

interface PlanAccumulator {
  id: string;
  provider: string;
  plan: string;
  account: string;
  employees: Map<string, EmployeeUsageItem>;
  modelTokens: Map<string, number>;
  modelEmployees: Map<string, Set<string>>;
  evidenceParts: Set<InventoryEvidence>;
  quota: ModelPlanQuotaSnapshot | null;
}

const BAILIAN_PERSONAL_PLAN_KEY = "阿里云百炼:bailian-token-plan-personal";

// William confirmed that this personal Bailian window was exhausted on
// 2026-08-14. The token figure is the matching HiveCrew execution-receipt
// total observed at that boundary (input + output + cache tokens), so it is
// an empirical calibration rather than a provider-published contractual cap.
const CONFIRMED_QUOTA_SNAPSHOTS: Readonly<
  Record<string, ModelPlanQuotaSnapshot>
> = {
  [BAILIAN_PERSONAL_PLAN_KEY]: {
    windowDays: 7,
    totalTokens: 415_592_437,
    usedTokens: 415_592_437,
    remainingTokens: 0,
    usedRatio: 1,
    resetAt: null,
    observedAt: "2026-08-14T13:28:05+08:00",
    evidence: "owner_confirmed_exhaustion_and_workspace_observation",
  },
};

function classifyAgentPlan(
  agent: Pick<Agent, "model" | "runtime_id">,
  runtime: AgentRuntime | null,
  observedModel?: string,
): ModelPlanClassification {
  const configuredModel = agent.model?.trim() ?? "";
  const model = observedModel?.trim() || configuredModel;
  const modelLower = model.toLowerCase();
  const runtimeName = runtime?.name.trim() ?? "";
  const runtimeLower = runtimeName.toLowerCase();
  const accountKey =
    runtime?.profile_id?.trim() || runtime?.id || modelLower || "unbound";

  const classified = (
    provider: string,
    plan: string,
    account = runtimeName || plan,
    stableAccountKey = accountKey,
  ): ModelPlanClassification => ({
    key: `${provider}:${stableAccountKey}`,
    provider,
    plan,
    account,
  });

  // A Bailian plan is encoded in the configured/observed model route. It is
  // stronger evidence than the qwen-compatible carrier label on the Runtime.
  if (modelLower.startsWith("bailian-token-plan-personal/")) {
    return classified(
      "阿里云百炼",
      "Token Plan Personal",
      "bailian-token-plan-personal",
      "bailian-token-plan-personal",
    );
  }

  // The configured model is stronger than its qwen-compatible carrier here:
  // Atlas points at a local DGX checkpoint, not a Qwen cloud billing plan.
  if (modelLower.includes("qwen3.6-27b-nvfp4")) {
    return classified("本地模型 · DGX", "qwen3.6-27B NVFP4");
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

function usageTokens(row: DashboardUsageByAgent) {
  return (
    row.input_tokens +
    row.output_tokens +
    row.cache_read_tokens +
    row.cache_write_tokens
  );
}

function inventoryEvidence(agent: Agent, runtime: AgentRuntime | null) {
  return agent.model?.trim()
    ? runtime
      ? ("model_and_runtime" as const)
      : ("model_only" as const)
    : ("runtime_only" as const);
}

function ensurePlan(
  groupedPlans: Map<string, PlanAccumulator>,
  classification: ModelPlanClassification,
) {
  const existing = groupedPlans.get(classification.key);
  if (existing) return existing;
  const created: PlanAccumulator = {
    id: classification.key,
    provider: classification.provider,
    plan: classification.plan,
    account: classification.account,
    employees: new Map(),
    modelTokens: new Map(),
    modelEmployees: new Map(),
    evidenceParts: new Set(),
    quota: CONFIRMED_QUOTA_SNAPSHOTS[classification.key] ?? null,
  };
  groupedPlans.set(classification.key, created);
  return created;
}

function addEmployee(
  plan: PlanAccumulator,
  agent: Agent,
  model: string,
  tokens: number,
) {
  const current = plan.employees.get(agent.id) ?? {
    id: agent.id,
    name: agent.name,
    model,
    observedTokens: 0,
  };
  current.observedTokens += tokens;
  plan.employees.set(agent.id, current);

  const modelName = model || "未标记模型";
  plan.modelTokens.set(
    modelName,
    (plan.modelTokens.get(modelName) ?? 0) + tokens,
  );
  const employeeIds = plan.modelEmployees.get(modelName) ?? new Set<string>();
  employeeIds.add(agent.id);
  plan.modelEmployees.set(modelName, employeeIds);
}

function aggregateModelRows(rows: readonly ModelUsageItem[]) {
  const models = new Map<string, ModelUsageItem>();
  for (const row of rows) {
    const current = models.get(row.model) ?? {
      model: row.model,
      observedTokens: 0,
      employeeCount: 0,
    };
    current.observedTokens += row.observedTokens;
    current.employeeCount += row.employeeCount;
    models.set(row.model, current);
  }
  return Array.from(models.values()).toSorted(
    (a, b) => b.observedTokens - a.observedTokens || a.model.localeCompare(b.model),
  );
}

export function buildModelQuotaUsageInventory(
  agents: readonly Agent[],
  runtimes: readonly AgentRuntime[],
  usageRows: readonly DashboardUsageByAgent[],
): ModelQuotaUsageInventory {
  const runtimeById = new Map(runtimes.map((runtime) => [runtime.id, runtime]));
  const agentById = new Map(agents.map((agent) => [agent.id, agent]));
  const groupedPlans = new Map<string, PlanAccumulator>();

  // Every current employee remains visible even before it produces usage.
  for (const agent of agents) {
    const runtime = agent.runtime_id
      ? (runtimeById.get(agent.runtime_id) ?? null)
      : null;
    const classification = classifyAgentPlan(agent, runtime);
    const plan = ensurePlan(groupedPlans, classification);
    plan.evidenceParts.add(inventoryEvidence(agent, runtime));
    addEmployee(plan, agent, agent.model?.trim() ?? "", 0);
  }

  // Usage is classified by the model recorded on the execution receipt, not
  // by the employee's current model field. This prevents a model switch from
  // relabelling historical Qwen Tokens as Bailian DeepSeek usage.
  for (const usage of usageRows) {
    const agent = agentById.get(usage.agent_id);
    if (!agent) continue;
    const runtime = agent.runtime_id
      ? (runtimeById.get(agent.runtime_id) ?? null)
      : null;
    const classification = classifyAgentPlan(agent, runtime, usage.model);
    const plan = ensurePlan(groupedPlans, classification);
    plan.evidenceParts.add(inventoryEvidence(agent, runtime));
    addEmployee(plan, agent, usage.model, usageTokens(usage));
  }

  const plans = Array.from(groupedPlans.values()).map(
    (plan): ModelPlanUsageRow => {
      const models = Array.from(plan.modelTokens, ([model, observedTokens]) => ({
        model,
        observedTokens,
        employeeCount: plan.modelEmployees.get(model)?.size ?? 0,
      })).toSorted(
        (a, b) =>
          b.observedTokens - a.observedTokens || a.model.localeCompare(b.model),
      );
      const employees = Array.from(plan.employees.values()).toSorted(
        (a, b) =>
          b.observedTokens - a.observedTokens || a.name.localeCompare(b.name),
      );
      const evidence = plan.evidenceParts.has("model_and_runtime")
        ? "model_and_runtime"
        : plan.evidenceParts.has("model_only")
          ? "model_only"
          : "runtime_only";
      return {
        id: plan.id,
        provider: plan.provider,
        plan: plan.plan,
        account: plan.account,
        employees,
        models,
        observedTokens: models.reduce(
          (total, model) => total + model.observedTokens,
          0,
        ),
        evidence,
        quota: plan.quota,
      };
    },
  );

  const providerMap = new Map<
    string,
    ModelProviderUsageGroup & { employeeIds: Set<string> }
  >();
  for (const plan of plans) {
    const provider = providerMap.get(plan.provider) ?? {
      provider: plan.provider,
      plans: [],
      observedTokens: 0,
      employeeCount: 0,
      employeeIds: new Set<string>(),
    };
    provider.plans.push(plan);
    provider.observedTokens += plan.observedTokens;
    for (const employee of plan.employees) provider.employeeIds.add(employee.id);
    provider.employeeCount = provider.employeeIds.size;
    providerMap.set(plan.provider, provider);
  }

  const providers = Array.from(providerMap.values())
    .map(({ employeeIds: _employeeIds, ...provider }) => ({
      ...provider,
      plans: provider.plans.toSorted(
        (a, b) => b.observedTokens - a.observedTokens || a.plan.localeCompare(b.plan),
      ),
    }))
    .toSorted(
      (a, b) =>
        b.observedTokens - a.observedTokens || a.provider.localeCompare(b.provider),
    );

  return {
    totalObservedTokens: plans.reduce(
      (total, plan) => total + plan.observedTokens,
      0,
    ),
    employeeCount: agents.length,
    planCount: plans.length,
    providers,
    models: aggregateModelRows(plans.flatMap((plan) => plan.models)),
  };
}
