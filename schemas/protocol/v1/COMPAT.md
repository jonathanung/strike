# Strike protocol schema compatibility

Generated from `pkg/protocol`. Do not edit by hand. Regenerate with `make protocol-schema`.

- Protocol version: `1.21.0`
- Legacy version when an envelope omits `v`: `1.0.0`
- JSON Schema: [`schema.json`](schema.json) (`https://raw.githubusercontent.com/jonathanung/strike/main/schemas/protocol/v1/schema.json`)
- Rust types: `webui/protocol-schema` (crate `protocol-schema`)

## Rules

- **Unknown event type strings are valid.** Decode them as an unknown event and keep the raw `data`. Do not fail the stream.
- **Unknown JSON fields are ignored** on events, ops, and nested objects.
- **Missing fields are zero values.** `required` in the schema is the writer contract for this version.
- **Unknown op type strings are rejected.** Client input stays fail-closed.
- **Named string fields are open.** `x-strike-known-values` lists what this version writes. Readers accept other strings.
- **Minor** `protocol.Version` bumps add types or optional fields. **Major** bumps rename, remove, or change meaning. **Patch** bumps do not change encoded JSON.
- Nil Go slices and maps encode as JSON `null`. Readers treat `null` like an empty list or map.

## Event types (63)

- `admission.decided` → `AdmissionDecided`
- `agent.contract.timeout` → `AgentContractTimeout`
- `agent.message` → `AgentMessage`
- `agent.selected` → `AgentSelected`
- `artifact.updated` → `ArtifactUpdated`
- `autonomy.selected` → `AutonomySelected`
- `child.completed` → `ChildCompleted`
- `child.escalated` → `ChildEscalated`
- `child.started` → `ChildStarted`
- `compaction.completed` → `CompactionCompleted`
- `compaction.started` → `CompactionStarted`
- `context.controls` → `ContextControlsSelected`
- `context.fit_warning` → `ContextFitWarning`
- `delegation.changed` → `DelegationChanged`
- `diagnostic.bundle` → `DiagnosticBundle`
- `effort.selected` → `EffortSelected`
- `engine.error` → `EngineError`
- `fast.selected` → `FastSelected`
- `files.invalidated` → `FilesInvalidated`
- `harness.progress` → `HarnessProgress`
- `hook.matched` → `HookMatched`
- `input.queued` → `InputQueued`
- `ledger.updated` → `LedgerUpdated`
- `model.selected` → `ModelSelected`
- `path.overlap` → `PathOverlap`
- `permission.asked` → `PermissionAsked`
- `permission.decided` → `PermissionDecided`
- `permission.mode` → `PermissionModeSelected`
- `permission.resolved` → `PermissionResolved`
- `phase.changed` → `PhaseChanged`
- `phase.grant_approved` → `PhaseGrantApproved`
- `plan.handoff` → `PlanHandoff`
- `process.exited` → `ProcessExited`
- `process.output` → `ProcessOutput`
- `process.started` → `ProcessStarted`
- `prompt.effective` → `EffectivePrompt`
- `provider.retrying` → `ProviderRetrying`
- `question.asked` → `QuestionAsked`
- `question.resolved` → `QuestionResolved`
- `reasoning.delta` → `ReasoningDelta`
- `scheduler.admitted` → `SchedulerAdmitted`
- `scheduler.canceled` → `SchedulerCanceled`
- `scheduler.queued` → `SchedulerQueued`
- `session.budget_warning` → `SessionBudgetWarning`
- `session.meta` → `SessionMeta`
- `session.rewound` → `SessionRewound`
- `session.titled` → `SessionTitled`
- `team.roster` → `TeamRoster`
- `text.delta` → `TextDelta`
- `tool.begin` → `ToolCallBegin`
- `tool.end` → `ToolCallEnd`
- `tool.loop_detected` → `ToolLoopDetected`
- `tool.output` → `ToolCallOutput`
- `tool.retrying` → `ToolRetrying`
- `turn.completed` → `TurnCompleted`
- `turn.started` → `TurnStarted`
- `turn.steered` → `TurnSteered`
- `usage.reported` → `UsageReported`
- `user.message` → `UserMessage`
- `verification.completed` → `VerificationCompleted`
- `verification.started` → `VerificationStarted`
- `wait.resolved` → `WaitResolved`
- `wait.started` → `WaitStarted`

## Op types (31)

- `compact` → `Compact`
- `context.controls` → `SetContextControls`
- `files.changed` → `FilesChanged`
- `input.queue_cancel` → `InputQueueCancel`
- `input.queue_edit` → `InputQueueEdit`
- `input.queue_reorder` → `InputQueueReorder`
- `input.queue_send_now` → `InputQueueSendNow`
- `inspect.diagnostic` → `InspectDiagnosticBundle`
- `inspect.prompt` → `InspectEffectivePrompt`
- `interrupt` → `Interrupt`
- `permission.reply` → `PermissionReply`
- `question.reply` → `QuestionReply`
- `rewind` → `Rewind`
- `select.agent` → `SelectAgent`
- `select.model` → `SelectModel`
- `set.autonomy` → `SetAutonomy`
- `set.effort` → `SetEffort`
- `set.fast` → `SetFast`
- `set.permission_mode` → `SetPermissionMode`
- `steer` → `Steer`
- `team.board_claim` → `TeamBoardClaim`
- `team.board_complete` → `TeamBoardComplete`
- `team.board_create` → `TeamBoardCreate`
- `team.broadcast` → `TeamBroadcast`
- `team.child_interrupt` → `TeamChildInterrupt`
- `team.message` → `TeamMessage`
- `team.spawn` → `TeamSpawn`
- `team.task_transition` → `TeamTaskTransition`
- `user.input` → `UserInput`
- `workflow.start` → `StartWorkflow`
- `workflow.stop` → `StopWorkflow`

## Versioning

Human rules and the drift check: [docs/protocol-schema.md](../../../docs/protocol-schema.md).
