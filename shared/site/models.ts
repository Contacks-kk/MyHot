// 这个站用哪些模型。框架自带一个 `default`：环境变量 LLM_BASE_URL / LLM_API_KEY / LLM_MODEL 指定的模型。
// 这里再列出具名的模型（每个用自己的地址和密钥环境变量），以及每一步默认用哪个；没写的步骤用 default。
// 部署时还可以用环境变量（PREFILTER_MODEL、SCORE_MODEL……）或后台的“模型与评测”页逐步改选。
import modelCatalog from "./model-catalog.json" with { type: "json" };

export interface ModelPreset {
  service: string;
  model: string;
  baseUrlEnv: string;
  apiKeyEnv: string;
  /** 额外的请求字段，比如短小的结构化任务关掉推理。 */
  extra?: Record<string, unknown>;
  /** 推理模型先想再答，额外给推理留的输出额度（token），加在每一步自己的额度上。default 用环境变量 LLM_REASONING_TOKENS。 */
  reasoningTokens?: number;
  /** 接口支持 JSON 模式。 */
  jsonMode: boolean;
  /** 能看图。 */
  vision?: boolean;
}

/** 具名的模型示例（每个要配自己的密钥）。用不上可以删掉。 */
export const PRESETS = modelCatalog.presets as Record<string, ModelPreset>;

/**
 * 每一步默认用的模型（步骤见后台“模型与评测”页），值是上面的名字或 default。没写的步骤用 default。
 * 例：{ score: "glm-5.3-flash-selection", groupReview: "mimo-v2.6-flash" }
 */
export const DEFAULTS = modelCatalog.defaults as Record<string, string>;

export interface ModelCapability {
  label: string;
  env: string;
  default: string;
  purposes: string[];
  vision?: boolean;
}

export const CAPABILITIES = modelCatalog.capabilities.map(({ key, ...capability }) => ({
  ...capability,
  default: DEFAULTS[key] ?? capability.default,
})) as Array<ModelCapability & { key: string }>;
