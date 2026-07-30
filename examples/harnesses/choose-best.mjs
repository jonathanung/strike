import { runHarness } from "../../sdk/typescript/index.mjs";

export async function chooseBest(input, provider, emit) {
  const candidates = [];
  for (let i = 0; i < 3; i++) {
    input.signal.throwIfAborted();
    const response = await provider.call({
      ...input.request,
      messages: [
        ...input.request.messages,
        { role: "user", text: `Generate candidate ${i + 1}` },
      ],
    });
    candidates.push(response.text);
    emit({ kind: "candidate", current: i + 1, total: 3 });
  }

  const text = candidates.reduce(
    (best, candidate) => candidate.length > best.length ? candidate : best,
    "",
  );
  return { text, stopReason: "end_turn" };
}

runHarness(chooseBest);
