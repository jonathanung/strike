import Lean.Data.Json

namespace StrikeHarness

open Lean

structure Input where
  request : Json

structure Provider where
  call : Json → IO Json
  /-- Brokered tool execution through Strike (additive; unused by provider-only harnesses). -/
  executeTool : Json → IO Json

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
    (stdin : IO.FS.Stream) (invocationId callId expectedType : String) : IO Json := do
  let response ← readMessage stdin
  let type ← stringField response "type"
  if type == "harness.cancel" then
    let reason := (response.getObjValD "reason").getStr?.toOption.getD "harness canceled"
    throw (IO.userError reason)
  if type != expectedType then
    awaitResult stdin invocationId callId expectedType
  else if (← stringField response "invocationId") != invocationId then
    throw (IO.userError s!"{expectedType} has the wrong invocationId")
  else if (← stringField response "callId") != callId then
    awaitResult stdin invocationId callId expectedType
  else
    match response.getObjVal? "error" with
    | .ok value =>
      -- tool.result may carry structured isError without a transport error
      if expectedType == "tool.result" then
        let isError := match response.getObjVal? "isError" with
          | .ok v => v.getBool?.toOption.getD false
          | .error _ => false
        let hasOutput := match response.getObjVal? "output" with
          | .ok _ => true
          | .error _ => false
        if isError || hasOutput then
          pure response
        else
          throw (IO.userError (value.getStr?.toOption.getD value.compress))
      else
        throw (IO.userError (value.getStr?.toOption.getD value.compress))
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
    awaitResult stdin invocationId callId "provider.result"
  let executeTool := fun toolCall => do
    let current := (← sequence.get) + 1
    sequence.set current
    let callId := s!"tool-{current}"
    let name := (toolCall.getObjValD "name").getStr?.toOption.getD ""
    let arguments := toolCall.getObjValD "arguments"
    let toolCallId := (toolCall.getObjValD "id").getStr?.toOption.getD callId
    send stdout (message invocationId "tool.execute" [
      ("callId", toJson callId),
      ("toolCallId", toJson toolCallId),
      ("name", toJson name),
      ("arguments", arguments)
    ])
    awaitResult stdin invocationId callId "tool.result"
  let emit := fun payload =>
    send stdout (message invocationId "progress.emit" [("payload", payload)])
  try
    let result ← harness { request } { call, executeTool } emit
    send stdout (message invocationId "harness.complete" [
      ("text", result.getObjValD "text"),
      ("reasoning", result.getObjValD "reasoning"),
      ("toolCalls", result.getObjValD "toolCalls"),
      ("stopReason", result.getObjValD "stopReason")
    ])
  catch error =>
    send stdout (message invocationId "harness.error" [("error", toJson error.toString)])

end StrikeHarness
