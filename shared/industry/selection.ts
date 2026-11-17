import selection from "../../backend-go/config/selection.json" with { type: "json" };

// 精选的门槛。评分标准本身写在 prompts/selection-score.md；这里只决定“多少分算入选”。
// 每篇资料由评分模型独立打两次分（0–100），两次之和 ≥ 2 × 门槛、并确认不是精选里已有新闻的重复报道才进精选
// （见 docs/selection.md），卡片上显示两次的平均分。
// 门槛按信源分级区分：官方一手信源的门槛低一些，媒体和个人的高一些。改了门槛或评分提示词，
// 用 legacy/ts-compat/tools/eval-selection.ts 在你自己标注的样本上重跑一遍，再决定上线（见 docs/selection.md）。

export const SELECTION = selection as { thresholds: Record<string, number>; understandFloor: number };
