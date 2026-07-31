import StrikeHarness

open Lean StrikeHarness

private def candidateRequest (request : Json) (number : Nat) : Json :=
  let messages := (request.getObjValD "messages").getArr?.toOption.getD #[]
  request.setObjVal! "messages" (.arr (messages.push (Json.mkObj [
    ("role", "user"),
    ("text", s!"Generate candidate {number}")
  ])))

private def chooseBest (input : Input) (provider : Provider) (emit : Emit) : IO Json := do
  let mut best := ""
  for number in [1, 2, 3] do
    let response ← provider.call (candidateRequest input.request number)
    let text ← IO.ofExcept (response.getObjVal? "text" >>= Json.getStr?)
    if text.length > best.length then
      best := text
    emit (Json.mkObj [
      ("kind", "candidate"),
      ("current", toJson number),
      ("total", toJson 3)
    ])
  pure (Json.mkObj [("text", best), ("stopReason", "end_turn")])

def main : IO Unit := runHarness chooseBest
