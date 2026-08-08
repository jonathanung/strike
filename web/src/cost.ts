/** Shared session cost/usage formatting for header chrome and /cost. */

export type CostStatus = {
  provider?: string;
  model?: string;
  contextUsed?: number;
  contextLimit?: number;
  inputTokens?: number;
  outputTokens?: number;
  cacheReadTokens?: number;
  usageSource?: string;
};

export type CostRates = { inputPerM: number; outputPerM: number; hasCost: boolean };

/** Never invents a fake $0 when pricing is unknown. */
export function formatCostNotice(status: CostStatus, rates?: CostRates): string {
  const ctx =
    status.contextUsed !== undefined && status.contextLimit !== undefined
      ? `${status.contextUsed.toLocaleString()} / ${status.contextLimit.toLocaleString()}`
      : status.contextUsed !== undefined
        ? `${status.contextUsed.toLocaleString()} used`
        : "not reported";
  const tokenBits: string[] = [];
  if (status.inputTokens !== undefined) tokenBits.push(`in ${status.inputTokens.toLocaleString()}`);
  if (status.outputTokens !== undefined) tokenBits.push(`out ${status.outputTokens.toLocaleString()}`);
  if (status.cacheReadTokens !== undefined) tokenBits.push(`cache ${status.cacheReadTokens.toLocaleString()}`);
  let costLine = "Cost: unknown (no pricing catalog for this model)";
  if (rates?.hasCost && (status.inputTokens !== undefined || status.outputTokens !== undefined)) {
    let total = 0;
    let ok = false;
    if (status.inputTokens !== undefined) {
      total += (status.inputTokens * rates.inputPerM) / 1_000_000;
      ok = true;
    }
    if (status.outputTokens !== undefined) {
      total += (status.outputTokens * rates.outputPerM) / 1_000_000;
      ok = true;
    }
    if (ok) {
      let usd: string;
      if (total > 0 && total < 0.01) usd = "<$0.01";
      else {
        let s = total.toFixed(4).replace(/0+$/, "").replace(/\.$/, "");
        if (!s || s === "-") s = "0";
        usd = `$${s}`;
      }
      if (status.inputTokens === undefined || status.outputTokens === undefined) usd += " (partial)";
      if (status.usageSource === "estimated" || status.usageSource?.startsWith("mixed")) usd += " est.";
      costLine = `Cost: ${usd}`;
    }
  } else if (!rates?.hasCost && tokenBits.length) {
    costLine = "Cost: unknown (pricing not in catalog)";
  } else if (!tokenBits.length) {
    costLine = "Cost: not reported";
  }
  return [
    `Provider: ${status.provider || "—"}`,
    `Model: ${status.model || "—"}`,
    `Context: ${ctx}`,
    `Tokens: ${tokenBits.length ? tokenBits.join(" · ") : "not reported"}`,
    costLine,
  ].join("\n");
}

export function formatCostLabel(status: CostStatus, rates?: CostRates & { context?: number }): string {
  const parts: string[] = [];
  if (status.inputTokens !== undefined) parts.push(`in ${status.inputTokens.toLocaleString()}`);
  if (status.outputTokens !== undefined) parts.push(`out ${status.outputTokens.toLocaleString()}`);
  if (status.cacheReadTokens !== undefined) parts.push(`cache ${status.cacheReadTokens.toLocaleString()}`);
  if (!parts.length) return "not reported";
  let usd = "";
  if (rates?.hasCost) {
    let total = 0;
    let ok = false;
    if (status.inputTokens !== undefined) {
      total += (status.inputTokens * rates.inputPerM) / 1_000_000;
      ok = true;
    }
    if (status.outputTokens !== undefined) {
      total += (status.outputTokens * rates.outputPerM) / 1_000_000;
      ok = true;
    }
    if (ok) {
      if (total > 0 && total < 0.01) usd = "<$0.01";
      else {
        let s = total.toFixed(4).replace(/0+$/, "").replace(/\.$/, "");
        if (!s || s === "-") s = "0";
        usd = `$${s}`;
      }
      if (status.inputTokens === undefined || status.outputTokens === undefined) usd += " partial";
      if (status.usageSource === "estimated" || status.usageSource?.startsWith("mixed")) usd += " est.";
    }
  }
  return usd ? `${usd} · ${parts.join(" · ")}` : parts.join(" · ");
}
