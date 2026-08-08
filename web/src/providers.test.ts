
import { describe, expect, it } from "vitest";
import { modelId, modelMetaLine, type ModelInfo } from "./providers";

describe("model metadata display", () => {
  it("prefers wire casing and keeps unknown as unknown", () => {
    expect(modelId({ ID: "gpt" })).toBe("gpt");
    expect(modelMetaLine({})).toBe("limits unknown");
  });

  it("formats known limits without inventing zeros as known", () => {
    const m: ModelInfo = {
      id: "m1",
      context: 128000,
      output: 4096,
      toolCall: true,
      reasoning: true,
      hasCost: true,
      inputCost: 1,
      outputCost: 2,
      source: "catalog",
    };
    const line = modelMetaLine(m);
    expect(line).toContain("ctx");
    expect(line).toContain("tools");
    expect(line).toContain("reasoning");
    expect(line).toContain("catalog");
    expect(line).toContain("$1/$2 per M");
  });
});
