import { describe, expect, it } from "vitest";
import {
  applyBoardClaim,
  applyBoardComplete,
  applyBoardCreate,
  hasTeamOp,
  newIdempotencyKey,
  teamControlEnabled,
  teamControlUnavailableReason,
  unavailableMessage,
} from "./teamControl";

describe("teamControl helpers", () => {
  it("detects advertised ops and capability gates", () => {
    const ops = ["team.spawn", "team.message", "user.input"];
    expect(hasTeamOp(ops, "team.spawn")).toBe(true);
    expect(hasTeamOp(ops, "team.board_create")).toBe(false);
    expect(teamControlEnabled(ops, { teamControl: true }, false)).toBe(true);
    expect(teamControlEnabled(ops, { teamControl: true }, true)).toBe(false);
    expect(teamControlEnabled([], { teamControl: true }, false)).toBe(false);
    expect(teamControlUnavailableReason({ attachOnly: true })).toBe("attach-only");
    expect(teamControlUnavailableReason({ teamControl: false, protocolOps: ops })).toBe("no-capability");
    expect(unavailableMessage("attach-only")).toMatch(/Attach-only/);
  });

  it("generates unique idempotency keys", () => {
    const a = newIdempotencyKey();
    const b = newIdempotencyKey();
    expect(a).toBeTruthy();
    expect(a).not.toBe(b);
  });

  it("updates local board task list without duplicates", () => {
    let list = applyBoardCreate([], "t1", "Ship it", 1);
    list = applyBoardCreate(list, "t1", "Ship it", 1);
    expect(list).toHaveLength(1);
    list = applyBoardClaim(list, "t1", 2);
    expect(list[0].status).toBe("claimed");
    expect(list[0].version).toBe(2);
    list = applyBoardComplete(list, "t1", 3);
    expect(list[0].status).toBe("completed");
    expect(list[0].version).toBe(3);
  });
});
