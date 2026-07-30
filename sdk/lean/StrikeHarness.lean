import Lean.Data.Json

namespace StrikeHarness

open Lean

structure Input where
  request : Json

structure Provider where
  call : Json → IO Json

abbrev Emit := Json → IO Unit
abbrev Harness := Input → Provider → Emit → IO Json

private def field (message : Json) (name : String) : IO Json :=
  IO.ofExcept (message.getObjVal? name)

private def stringField (message : Json) (name : String) : IO String := do
  IO.ofExcept (← field message name).getStr?

private def readMessage (stdin : IO.FS.Stream) : IO Json := do
  let line ← stdin.getLine
  IO.ofExcept (Json.parse line)

private def send (stdout : IO.FS.Stream) (message : Json) : IO Unit := do
  stdout.putStrLn message.compress
  stdout.flush

private def message (invocationId type : String) (fields : List (String × Json) := []) : Json :=
  Json.mkObj ([
    ("version", toJson 1),
    ("type", toJson type),
    ("invocationId", toJson invocationId)
  ] ++ fields)

private partial def awaitResult
    (stdin : IO.FS.Stream) (invocationId callId : String) : IO Json := do
  let response ← readMessage stdin
  let type ← stringField response "type"
  if type == "harness.cancel" then
    let reason := (response.getObjValD "reason").getStr?.toOption.getD "harness canceled"
    throw (IO.userError reason)
  if type != "provider.result" then
    awaitResult stdin invocationId callId
  else if (← stringField response "invocationId") != invocationId then
    throw (IO.userError "provider result has the wrong invocationId")
  else if (← stringField response "callId") != callId then
    awaitResult stdin invocationId callId
  else
    match response.getObjVal? "error" with
    | .ok value => throw (IO.userError (value.getStr?.toOption.getD value.compress))
    | .error _ => pure response

/-- Runs one ordinary Lean harness over Strike's JSONL subprocess transport. -/
def runHarness (harness : Harness) : IO Unit := do
  let stdin ← IO.getStdin
  let stdout ← IO.getStdout
  let start ← readMessage stdin
  if (← stringField start "type") != "harness.start" then
    throw (IO.userError "expected harness.start")
  let invocationId ← stringField start "invocationId"
  let request ← field start "request"
  let sequence ← IO.mkRef 0
  let call := fun request => do
    let current := (← sequence.get) + 1
    sequence.set current
    let callId := s!"provider-{current}"
    send stdout (message invocationId "provider.call" [
      ("callId", toJson callId),
      ("request", request)
    ])
    awaitResult stdin invocationId callId
  let emit := fun payload =>
    send stdout (message invocationId "progress.emit" [("payload", payload)])
  try
    let result ← harness { request } { call } emit
    send stdout ((message invocationId "harness.complete").mergeObj result)
  catch error =>
    send stdout (message invocationId "harness.error" [("error", toJson error.toString)])

end StrikeHarness
