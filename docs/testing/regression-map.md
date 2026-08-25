# Regression Map

This map is the handoff index for agents changing Codex control-plane
contracts, Telegram routing, observer panels, lifecycle recovery, diagnostics,
or Plan Mode.

When behavior changes, update the relevant ADR first, then update or add the tests named here. The tests are part of the architecture: they describe the contract that must survive App Server drift and daemon restarts.

## AFC And Writer Ownership

ADR: `docs/adr/ADR-020-afc-writer-ownership.md`.

Primary tests:

- `internal/appserver/client_test.go::TestClientSerializesConcurrentJSONRPCWrites`
- `internal/appserver/writer_manager_test.go::TestThreadClaimRegistryRejectsCrossWriterOwnership`
- `internal/appserver/writer_manager_test.go::TestWriterManagerConcurrentReservationsShareOneStart`
- `internal/appserver/writer_manager_test.go::TestWriterManagerKeepsClaimsUntilLastTerminalClosesProcess`
- `internal/appserver/writer_manager_test.go::TestWriterManagerUnknownDispatchBlocksCloseAndReplay`
- `internal/appserver/writer_manager_test.go::TestWriterManagerStartFailureReleasesGenerationClaims`
- `internal/appserver/writer_manager_test.go::TestWriterManagerClaimsThreadAfterUnclaimedProcessReservation`
- `internal/appserver/writer_manager_test.go::TestWriterManagerCloseFailureKeepsClaimsAndFailsClosed`
- `internal/daemon/service_test.go::TestEnsureSessionsStartsOnlyPollSession`
- `internal/daemon/service_test.go::TestBootstrapTrackedStateDoesNotResumeLegacyThreads`
- `internal/daemon/service_test.go::TestLegacyTurnLazilyStartsWriterAndClosesAfterTerminal`
- `internal/daemon/service_test.go::TestLegacyNewThreadClaimsReturnedIDBeforeFirstTurn`

Contract notes:

- A process-generation thread claim is reserved before a mutating App Server
  call and is released only after the owning process closes successfully.
- `unknown` dispatch is fail-closed and is never replayed automatically.
- Different writers may own different threads concurrently, but never the same
  thread.
- `thread/start` uses an unclaimed process reservation and claims the returned
  durable id before the first turn.
- `/sync off` must not restore the observer or start/resume an eager legacy
  lifecycle; explicit later legacy work may start its writer lazily.

## Shared Daemon AFC Sync

ADR: `docs/adr/ADR-026-shared-daemon-afc-sync.md`; design:
`docs/plans/2026-08-16-afc-desktop-sync-mvp-design.md`.

Restart boundary: `docs/adr/ADR-027-afc-reset-on-start-mvp.md`; reliability
amendment: `docs/adr/ADR-029-afc-reliability-and-external-delivery.md`; design:
`docs/plans/2026-08-19-afc-reset-on-start-mvp-design.md`.

Planned primary tests:

- `internal/config/config_test.go` covers daemon transport selection and the
  default/configured AFC initial topic limit.
- `internal/appserver/client_test.go` proves daemon mode connects directly over
  WebSocket on the Unix socket without spawning a process, spawned mode keeps
  `app-server --listen`, and `thread/resume` contains only `threadId`;
  serialized App Server messages omit the unsupported `jsonrpc` header.
- `internal/appserver/client_test.go::TestDaemonTransportKeepsConnectionAfterLargeThreadRead`
  proves a large local `thread/read` response does not close the shared-daemon
  WebSocket and later requests still succeed on the same connection.
- `internal/daemon/afc_test.go` proves AFC-active legacy mutations fail before
  App Server access and explicit post-off legacy work remains lazy.
- `internal/daemon/afc_test.go` proves initial activation uses the configured
  limit and active reconciliation creates exactly one topic for a new eligible
  Desktop thread.
- `internal/daemon/afc_test.go` proves restart/reconnect restores shared-daemon
  subscriptions without duplicate topics or prompt replay.
- `internal/daemon/afc_test.go` proves active topic messages steer the expected
  turn and stale-active recovery does not create a parallel turn.
- `internal/daemon/afc_test.go::TestAFCSharedDaemonRestartUnknownReconcilesBeforeSteer`
  proves bridge restart recovery adopts the authoritative active turn without
  replaying the old receipt or starting a parallel turn.
- `internal/storage/store_afc_test.go::TestResetAFCOnStartupMakesSessionCleanupOnlyAndPreservesOtherState`
  proves process startup resets only AFC state, makes old topics/drafts
  cleanup-only, and keeps receipts non-replayable.
- `internal/daemon/afc_test.go::TestAFCStartupResetsSessionCleansTopicsAndWarnsOnceWhenDaemonUnavailable`
  proves startup cleans old topics, resets persisted connection flags, and
  warns Control once when the configured shared daemon is unavailable.
- `internal/daemon/afc_test.go::TestAFCStartupDoesNotWarnWhenSharedDaemonConnects`
  proves a healthy shared-daemon startup emits no warning.
- `internal/daemon/afc_test.go::TestAFCManagedSteerFallsBackToNewTurnAfterAuthoritativeIdle`
  proves stale `turn/steer` rejection re-reads authority and starts exactly one
  replacement turn.
- `internal/daemon/afc_test.go::TestAFCAuthoritativeSupersedingTurnReleasesStaleLocalLease`
  proves an authoritative newer turn releases stale local AFC ownership.
- `internal/daemon/health_test.go::TestDaemonHeartbeatFailureTruthfullyResetsAFCWithoutReplay`
  proves a failed managed-daemon heartbeat reports disconnected state, resets
  AFC to `off`, and queues a Control warning without replay.
- `internal/daemon/health_test.go::TestHealthEpisodeQueuesOneWarningAndOneRecovery`
  proves health warnings and recoveries are queued for Telegram General without
  an invalid forum thread id.
- `internal/daemon/health_test.go::TestHealthDeliveryFallsBackToGeneralForInvalidTopic`
  proves a legacy health delivery targeting a missing topic retries once in
  General instead of becoming a silent dead letter.
- `internal/daemon/health_test.go::TestStatusShowsHeartbeatDeadLettersAndOpenHealthIncidents`,
  `internal/daemon/afc_test.go::TestAFCControlHelpListsNewTaskCommands`, and
  `internal/storage/store_test.go::TestDeliveryQueueDeadCount` prove both
  regular and active-AFC `/status` expose app-server heartbeat age, open
  incidents, and dead delivery count rather than showing only the retryable
  backlog.
- `internal/daemon/service_test.go::TestStartupRepairResetDiscardsRequestFromPreviousProcess`
  proves a process restart does not execute a stale repair request against the
  newly opened poll session.
- `internal/daemon/service_test.go::TestStalePollSessionErrorDoesNotRequestRepair`
  and `TestCurrentPollSessionErrorRequestsRepair` prove only the current poll
  generation can request another repair, preventing reconnect storms while
  preserving recovery from a real current-session failure.
- `internal/daemon/afc_test.go` proves Stop interrupts current Desktop-origin
  and Telegram-origin turns through guarded authoritative coordinates.
- `internal/daemon/afc_test.go::TestAFCLongFinalSplitsWithinTelegramLimit`
  proves long Finals are delivered as ordered UTF-16-safe chunks and committed
  only after all chunks succeed.
- `internal/daemon/afc_test.go::TestAFCLongFinalFailureKeepsFingerprintPending`
  proves a failed continuation remains pending for reconciliation retry.

Contract notes:

- Desktop and `codex-tg` share one managed App Server daemon through independent
  WebSocket connections to the same Unix socket.
- Shared-daemon mode is fail-closed and never silently spawns a private server.
- AFC activation creates five recent topics by default, configurable through
  `CTR_GO_AFC_INITIAL_TOPIC_LIMIT`, then continuously discovers new chats.
- Shared-daemon subscription resumes by exact `threadId` only; `thread/read`
  remains the durable catch-up source.
- A `codex-tg` process restart resets AFC to `off`, cleans the previous Telegram
  session, and requires a manual `/sync on`; it does not interrupt Codex work.
- Managed-daemon transport loss uses the same boundary. A heartbeat detects
  half-open poll connections; repair reconnects polling but never re-enables
  AFC automatically.
- Health and startup warnings are sent to Telegram General by omitting the
  forum topic id. A stale persisted health topic falls back to General when
  Telegram reports `message thread not found`.
- AFC active rejects legacy DM mutations before App Server access. Off does not
  restore legacy lifecycle; explicit later DM work may start lazily.
- Long AFC Finals are split at Telegram's UTF-16 message boundary. The first
  chunk carries the Final header; continuation chunks do not repeat it.

## AFC Forum Group Transport

ADR: `docs/adr/ADR-021-afc-forum-group-surface.md`.

Primary tests:

- `internal/telegram/api_test.go::TestClientForumTopicOperations`
- `internal/telegram/api_test.go::TestClientProbeForumGroupAndValidateSecurity`
- `internal/telegram/api_test.go::TestTelegramAPIErrorClassifiesTopicAndRetryFailures`
- `internal/telegram/api_test.go::TestClientPlainHTTPFailureRemainsTypedAndRetryable`

Contract notes:

- AFC uses one exact private forum supergroup containing only the allowed user
  and bot.
- The bot must be creator or an administrator with manage-topic and
  delete-message rights.
- Stale topic errors and retryable Telegram failures are distinct typed cases.
- General/Control is a manual permanent prerequisite and is never deleted by
  AFC cleanup.

## Control Plane Architecture

ADR: `docs/adr/ADR-019-codex-control-plane.md`; feature brief is
`docs/process/v0.5.0-codex-control-plane-brief.md`; roadmap is
`docs/process/v0.5.0-control-api-roadmap.md`.

Future primary tests:

- App Server capability-map smoke detects supported and missing schema methods.
- Control-core thread lifecycle tests cover list/read/start/resume/fork/rename
  through an App Server-backed implementation.
- Control-core turn lifecycle tests cover start/steer/interrupt plus stale-active
  recovery preservation.
- Event normalization tests cover lifecycle/tool/final/approval/input events
  before Telegram-specific rendering.
- Notification policy tests classify normalized events as `urgent`, `normal`,
  `silent`, or `digest`.
- Local control API tests prove the router-agent HTTP adapter is disabled unless
  configured, loopback-only, read-only for the first slice, and delegates
  thread list/read through the control surface.
- Telegram adapter compatibility tests prove existing commands and callback
  routing still work on top of the control API.

Contract notes:

- App Server remains authoritative for live interactive Codex state.
- SDK/MCP adapters are allowed only as orchestration adapters under ADR-019.
- Session JSONL must not re-enter live UI/control state.
- Generated App Server schema is an input to capability detection, not a file to
  commit as a runtime source of truth.
- Telegram is a channel adapter; Telegram ids must not become Codex identity.

## YMessenger Launch Requests

ADR: `docs/adr/ADR-028-ymessenger-launch-requests.md`; reliability amendment:
`docs/adr/ADR-029-afc-reliability-and-external-delivery.md`; feature brief:
`docs/process/ind-06-ymessenger-launch-requests-brief.md`.

Primary tests:

- `internal/config/config_test.go::TestFromEnvReadsYMessengerLaunchRequestConfig`
- `internal/config/config_test.go::TestValidateYMessengerRequiresEnabledFields`
- `internal/config/config_test.go::TestMarshalJSONRedactsYMessengerToken`
- `internal/config/config_test.go::TestYMessengerApprovalDefaultsToRequired`
- `internal/config/config_test.go::TestValidateYMessengerDoesNotRequireApprovalTopicWhenApprovalDisabled`
- `internal/ymessenger/client_test.go::TestClientGetUpdatesUsesOAuthTeamAndDecodesWireFormat`
- `internal/ymessenger/client_test.go::TestClientGetThreadRootUsesRobotOAuthAndExactTimestampWindow`
- `internal/ymessenger/client_test.go::TestClientGetThreadRootReturnsNilWhenExactMessageIsAbsent`
- `internal/ymessenger/client_test.go::TestClientGetThreadRootDoesNotTreatHistoryAPIErrorAsMissingRoot`
- `internal/ymessenger/client_test.go::TestClientSendExternalReplyTargetsInvocation`
- `internal/ymessenger/filter_test.go::TestRequestsFromUpdatesAcceptsAllowedMentionFromAnyChat`
- `internal/ymessenger/filter_test.go::TestRequestsFromUpdatesBuildsExplicitReplyContextAndReplyTarget`
- `internal/ymessenger/filter_test.go::TestRequestsFromUpdatesUsesHistoryThreadRootAndCanAutoStart`
- `internal/ymessenger/filter_test.go::TestRequestsFromUpdatesRepliesToUnauthorizedMentionWithoutLaunch`
- `internal/ymessenger/poller_test.go::TestPollOnceUsesPersistedCursorAndAdvancesPastIgnoredUpdates`
- `internal/ymessenger/poller_test.go::TestPollOnceFailureDoesNotAdvanceCursorAndNextCallRetriesSameOffset`
- `internal/ymessenger/poller_test.go::TestPollOnceResolvesActionableThreadRootFromHistory`
- `internal/ymessenger/poller_test.go::TestPollOnceHistoryFailureDoesNotAdvanceCursor`
- `internal/ymessenger/poller_test.go::TestPollOnceSkipsOneBrokenThreadRootAfterThreeAttempts`
- `internal/ymessenger/poller_test.go::TestPollOnceNeverSkipsHistoryAuthorizationFailure`
- `internal/storage/store_external_requests_test.go::TestIngestExternalLaunchRequestsCommitsRequestsAndCursorTogether`
- `internal/storage/store_external_requests_test.go::TestOpenDropsLegacyExternalSourceMessageCacheAndPreservesRequests`
- `internal/storage/store_external_requests_test.go::TestExternalReplyQueueIsIdempotentAndRetryable`
- `internal/storage/store_external_requests_test.go::TestExternalAckIsDurableRetryableAndRejectedRequestIsNotRendered`
- `internal/storage/store_external_requests_test.go::TestExternalLaunchRequestTransitionsAreConditional`
- `internal/storage/store_external_requests_test.go::TestRecoverStartingExternalLaunchRequestsMarksOutcomeUnknown`
- `internal/daemon/external_requests_test.go::TestExternalLaunchApprovalRendersOnceAndDismissEditsSameMessage`
- `internal/daemon/external_requests_test.go::TestExternalLaunchApprovalCallbackFailsClosedAndStartClaimsOnce`
- `internal/daemon/external_requests_test.go::TestExternalLaunchAutoStartSkipsApprovalAndClaimsDurably`
- `internal/daemon/external_requests_test.go::TestExternalFinalQueuesAndDeliversReplyToInvocation`
- `internal/daemon/external_requests_test.go::TestRejectedExternalSenderGetsOnlyPolicyReplyAndCannotStartCodex`
- `internal/daemon/health_test.go::TestHealthEpisodeQueuesOneWarningAndOneRecovery`
- `internal/daemon/external_requests_test.go::TestDispatchExternalLaunchRequestCreatesAFCThreadTurnAndTopic`
- `internal/daemon/external_requests_test.go::TestDispatchExternalLaunchRequestDoesNotReplayAmbiguousThreadStart`
- `internal/daemon/external_requests_test.go::TestDispatchExternalLaunchRequestLeavesPendingWhileAFCInactive`
- `cmd/ctr-go/main_test.go::TestYMessengerPollerRemainsDisabledByDefault`

Contract notes:

- `codex-tg` remains one process and the only Telegram update consumer.
- Yandex Messenger accepts configured senders in any source chat only when the
  configured robot is explicitly mentioned.
- Thread roots are fetched on demand through History API with the configured
  robot OAuth token; unrelated chat messages are not cached. Authorization
  failures never advance the cursor. One root-local failure degrades to an
  explicit unavailable marker after three attempts so it cannot block all
  later updates forever.
- The permanent approval topic is configured, never AFC-owned, and never
  deleted by AFC cleanup when approval is enabled.
- Auto-start is opt-in; sender allowlisting and explicit robot mention remain
  mandatory in both modes.
- Allowed mentions receive a durable acknowledgement and a durable terminal
  outcome. A terminal turn without final text receives an explicit fallback.
- A disallowed explicit mention receives one owner-only policy reply and cannot
  create Telegram approval or Codex work.
- Duplicate updates and callbacks are safe; ambiguous App Server dispatch is
  visible and non-replayable.

## Cron Launch Requests

ADR: `docs/adr/ADR-030-cron-launch-requests.md`; feature brief:
`docs/process/cron-launch-requests-brief.md`.

Primary tests:

- `internal/cronpoller/poller_test.go::TestPollOnceEnqueuesOneTelegramApprovalRequestAfterDailyCronTime`
- `internal/cronpoller/poller_test.go::TestPollOnceAfterFiveMissedDaysCreatesOnlyCurrentDayRequest`
- `internal/cronpoller/poller_test.go::TestPollOnceBeforeDailyCronTimeWaitsForToday`
- `internal/cronpoller/poller_test.go::TestPollOnceWeeklyScheduleCatchesUpOnceForCurrentWeek`
- `internal/cronpoller/poller_test.go::TestPollOnceWeeklyScheduleWaitsForCurrentWeekSlot`
- `internal/cronpoller/poller_test.go::TestPollOnceAutoPolicySkipsTelegramLaunchApproval`
- `internal/cronpoller/poller_test.go::TestPollOnceFailsClosedForInvalidOrUnsupportedSchedule`
- `internal/storage/store_external_requests_test.go::TestIngestExternalLaunchRequestsCommitsRequestsAndCursorTogether`
- `internal/daemon/external_requests_test.go::TestDispatchExternalLaunchRequestCreatesAFCThreadTurnAndTopic`

Contract notes:

- A due task owns one `(cron, task-id:scheduled-local-date)` identity across pending,
  dismissed, failed, started, and completed states.
- Missing days or weeks never create a backlog, and same-period daemon restarts
  cannot create another request.
- Cron execution settings are request-local and must not change global Codex
  model or reasoning settings.

## Distribution And Local Config

ADR: `docs/adr/ADR-017-release-binaries-and-init.md` and
`docs/adr/ADR-018-macos-service-installer.md`; feature briefs are
`docs/process/v0.3.0-distribution-brief.md` and
`docs/process/v0.4.0-macos-service-installer-brief.md`.

Primary tests:

- `internal/config/config_test.go::TestParseEnvFileSupportsCommentsAndQuotes`
- `internal/config/config_test.go::TestParseEnvFileRejectsInvalidLine`
- `internal/config/config_test.go::TestLoadReadsConfigFileAndEnvOverridesIt`
- `internal/config/config_test.go::TestLoadAppliesRuntimeProxyEnvFromConfigFile`
- `internal/config/config_test.go::TestLoadDoesNotOverrideExplicitRuntimeProxyEnv`
- `cmd/ctr-go/main_test.go::TestRunInitWritesPrivateConfigAndRefusesOverwrite`
- `cmd/ctr-go/main_test.go::TestRunInitForceOverwritesConfig`
- `cmd/ctr-go/main_test.go::TestStatusAndDoctorDoNotLeakConfigFileToken`
- `cmd/ctr-go/main_test.go::TestFatalErrorSanitizerRedactsTelegramBotURL`
- `cmd/ctr-go/service_test.go::TestServiceInstallNonInteractiveWritesConfigAndLaunchAgent`
- `cmd/ctr-go/service_test.go::TestServiceInstallCapturesRuntimeProxyEnvInConfig`
- `cmd/ctr-go/service_test.go::TestServiceInstallInteractiveWizardRetriesInvalidValues`
- `cmd/ctr-go/service_test.go::TestServiceInstallNonInteractiveReportsMissingFlags`
- `cmd/ctr-go/service_test.go::TestRenderLaunchAgentPlistContainsOnlyConfigEnvironment`
- `cmd/ctr-go/service_test.go::TestServiceLifecycleUsesLaunchctlRunner`
- `cmd/ctr-go/service_test.go::TestServiceStartAcceptsKickstartFailureWhenServiceLoaded`
- `internal/trayapp/actions_test.go::TestCTRGoArgs`
- `internal/trayapp/actions_test.go::TestServiceSetupArgs`

Contract notes:

- `config.env` is local runtime state and must not be committed.
- Explicit environment variables override config file values.
- macOS LaunchAgent plists must carry only `CTR_GO_CONFIG`, never token/user/cwd env values.
- Proxy env needed by the operator shell may be stored in the private config and
  applied by the process after startup.
- Tray control is not a settings editor in v0.4.0.
- Release archives and packages must not include local config, sessions, SQLite state, logs, or screenshots.

## Plan Mode Routing

ADR: `docs/adr/ADR-006-plan-prompt-mode.md`; reset addendum:
`docs/adr/ADR-016-plan-mode-reset-contract.md`

Primary tests:

- `internal/daemon/service_test.go::TestPlanCommandStartsPlanCollaborationMode`
- `internal/daemon/service_test.go::TestPlanCommandUsesBoundThreadWhenNoExplicitThread`
- `internal/daemon/service_test.go::TestPlanCommandUnknownHeadUsesBoundThreadAsPromptText`
- `internal/daemon/service_test.go::TestPlanCommandUnknownHeadWithoutImplicitRouteShowsUsage`
- `internal/daemon/service_test.go::TestPlanCommandUUIDLikeHeadStaysExplicit`
- `internal/daemon/service_test.go::TestPlanCommandKnownThreadHeadStaysExplicit`
- `internal/daemon/service_test.go::TestReplyPlanFlagStartsPlanCollaborationMode`
- `internal/daemon/service_test.go::TestReplyDefaultFlagStartsDefaultCollaborationMode`
- `internal/daemon/service_test.go::TestDefaultModeCommandStartsDefaultCollaborationMode`
- `internal/daemon/service_test.go::TestPlanFinalCardShowsTurnOffPlanButton`
- `internal/daemon/service_test.go::TestNormalFinalCardDoesNotShowTurnOffPlanButton`
- `internal/daemon/service_test.go::TestReplyCommandConsumesDefaultOverrideOnce`
- `internal/daemon/service_test.go::TestDefaultOverrideSurvivesTurnStartFailure`
- `internal/daemon/service_test.go::TestPlanCommandClearsStaleDefaultOverride`
- `internal/daemon/service_test.go::TestStopSetsDefaultOverrideForActiveThread`
- `internal/daemon/service_test.go::TestStopSetsDefaultOverrideForIdleThread`
- `internal/daemon/observer_ui_v2_test.go::TestTurnOffPlanCallbackSetsDefaultOverrideAndEditsFinalCard`
- `internal/daemon/observer_ui_v2_test.go::TestTurnOffPlanCallbackRejectsMismatchedMessageID`
- `internal/daemon/service_test.go::TestPlanModeCommandCanRouteByReply`
- `internal/daemon/service_test.go::TestPlainReplyToSyntheticPlanPromptUsesTurnSteer`
- `internal/daemon/service_test.go::TestPlainReplyToSyntheticPlanPromptFallsBackToTurnStart`
- `internal/daemon/service_test.go::TestPlainReplyToRealPlanPromptUsesServerRequest`
- `internal/daemon/observer_ui_v2_test.go::TestSyncThreadPanelCreatesRouteablePlanPromptAndDedupes`
- `internal/daemon/observer_ui_v2_test.go::TestSyncThreadPanelCreatesServerRequestPlanPromptRoute`

Live E2E:

- `tests/live_e2e/telegram_readback_e2e.py` case `plan_mode_reset`

Contract notes:

- `/plan <text>` uses reply, armed state, or bound thread routing.
- `/plan <thread> <text>` is explicit only for known or UUID-like thread ids.
- `/reply --plan <thread> <text>` remains strict.
- `Turn off Plan` on a Plan Final Card and `/stop <thread>` set a one-shot Default override for the next ordinary turn; they do not start a reset turn.
- The one-shot override is cleared after a successful ordinary `turn/start` and remains after a failed `turn/start`.
- Hidden `/default` and `/reply --default` fallback paths remain tested but are not advertised in public help or Telegram command menu.
- `Turn off Plan` is panel-bound like Details; stale panel/message/thread/turn callbacks fail closed without changing state.
- Plan choice buttons must stay scoped to the same turn as the `[Plan]` card.
- Stale pending `user_input` from an older turn must not add `answer_choice` buttons to a newer `[commentary]` panel.

## Project Thread Creation

ADR: `docs/adr/ADR-014-newchat-chat-folder-contract.md`; feature brief is `docs/process/create-thread-from-project-brief.md`.

Primary tests:

- `internal/daemon/service_test.go::TestProjectsCommandShowsProjectButtonsGroupedByCWD`
- `internal/daemon/service_test.go::TestIsCodexChatsCWDMatchesGenericMacAndWindowsPaths`
- `internal/daemon/service_test.go::TestProjectsCommandShowsChatsSectionAndSortsByRecency`
- `internal/daemon/service_test.go::TestProjectsPaginationUsesPreviewLimitsAndKeepsLatestChats`
- `internal/daemon/service_test.go::TestOpenChatsPaginatesAndChatSelectionBindsThread`
- `internal/daemon/service_test.go::TestProjectsCloseDeletesMenuMessage`
- `internal/daemon/service_test.go::TestProjectOpenShowsNewThreadMenu`
- `internal/daemon/service_test.go::TestProjectNewThreadArmsThenPlainTextCreatesThread`
- `internal/daemon/service_test.go::TestProjectNewThreadRejectsThreadStartWithoutID`
- `internal/daemon/service_test.go::TestProjectNewThreadTurnStartFailureSavesThread`
- `internal/daemon/service_test.go::TestNewChatCommandCreatesCodexUIChatCWDAndBinds`
- `internal/daemon/service_test.go::TestNewChatCWDUsesFallbackSlugAndCollisionSuffix`
- `internal/daemon/service_test.go::TestNewThreadCommandCreatesThreadWithoutCWDAndBinds`
- `internal/daemon/service_test.go::TestNewChatCommandRejectsMissingThreadID`
- `internal/daemon/service_test.go::TestNewChatCommandTurnStartFailureSavesAndBindsThread`
- `internal/daemon/service_test.go::TestNewThreadCommandTurnStartFailureSavesAndBindsThread`
- `internal/config/config_test.go::TestFromEnvReadsCodexChatsRoot`
- `internal/telegram/bot_test.go::TestDefaultCommandsExposeNewChatMenuCommand`
- `internal/daemon/service_test.go::TestSummaryPanelDoesNotShowStalePendingUserInputButtons`
- `tests/config_env_test.go::TestFromEnvProjectChatLimitsClampInvalidValues`

Live E2E:

- Open `/projects`, choose a project, press `New thread`, send a prompt, and verify a new thread/run reaches `[Final]`.
- Open `/projects`, verify normal projects are sorted by recent activity, `Documents/Codex` threads are shown only as latest Chat previews, then open full `Chats` pagination and select a Chat.
- Run `/newchat <prompt>`, verify the new thread reaches `[Final]`, the generated cwd exists under the configured Chats root, `/projects -> Open Chats` shows it, and a plain follow-up routes to the newly bound Chat thread.
- Run `/newthread <prompt>`, verify the new thread reaches `[Final]` without creating a Chat cwd under the configured Chats root.
- Send a plain reply after creation and verify it routes to the newly bound thread.
- Run a Plan Mode prompt with structured choices and verify choice buttons appear only on the current `[Plan]` card.

Contract notes:

- Project/workspace identity comes from cached thread `cwd`; this flow does not create or edit work directories.
- Threads under generic `Documents/Codex` cwd roots or configured `CTR_GO_CODEX_CHATS_ROOT` are Codex UI `Chats`, not normal projects. A Chat selection opens and binds its single thread; Chat lists do not expose project `New thread`.
- Main `/projects` uses project pagination with configurable preview limits and keeps latest Chat previews newest-first. Full Chat pagination lives behind `Open Chats`.
- Project buttons use `N. Project name`; Chat buttons use `Chat N. Thread name`. The menu must not render internal `key:` rows and must show each project row's `last thread:`.
- `/newchat <prompt>` creates a dated Chat cwd from a prompt slug and passes that cwd to App Server `thread/start`.
- `/newthread <prompt>` creates a new App Server thread without a Telegram-selected cwd parameter. It must not create a Chat folder; App Server may still attach the daemon default cwd.
- Telegram must not accept arbitrary local filesystem paths for thread creation.
- The first prompt is required; create-only threads are out of scope for this slice.

## Full Thread ID Access

ADR: `docs/adr/ADR-007-parallel-thread-visual-identity.md`

Primary tests:

- `internal/daemon/service_test.go::TestContextCardBoundThreadIncludesFullThreadID`
- `internal/daemon/service_test.go::TestSummaryPanelGetThreadIDButtonSendsCopyableIDs`
- `internal/daemon/service_test.go::TestFinalSummaryPanelHasGetThreadIDButton`
- `internal/daemon/service_test.go::TestFinalCardGetThreadIDButtonSendsCopyableIDs`

Contract notes:

- Header chips like `T:d663` and `R:d9bc` are visual hints only.
- Operators must be able to retrieve copyable full ids from Telegram without SQLite/log access.

## Final Card Details Binding

ADR: `docs/adr/ADR-004-final-card-details-ux.md`

Primary tests:

- `internal/daemon/observer_ui_v2_test.go::TestFinalCardDetailsCallbacksEditSameMessageAndExportToolsFile`
- `internal/daemon/observer_ui_v2_test.go::TestFinalCardDetailsShowsToolOnlyTurnWithoutCommentary`
- `internal/daemon/observer_ui_v2_test.go::TestDetailsCallbacksUsePanelTurnInsteadOfLatestThreadTurn`
- `internal/daemon/observer_ui_v2_test.go::TestDetailsCallbacksStayBoundToOriginalPanelAfterNewerRunCompletes`
- `internal/daemon/observer_ui_v2_test.go::TestDetailsCallbackWithoutPanelIDDoesNotFallbackToCurrentPanel`
- `internal/daemon/observer_ui_v2_test.go::TestDetailsCallbackRejectsMismatchedMessageID`
- `internal/daemon/observer_ui_v2_test.go::TestDetailsToolsFileRejectsMismatchedPanelRoute`

Contract notes:

- `Details`, pagination, `Tool on`, `Tools file`, and `Back` are bound to the completed panel/card that produced the callback.
- A Details callback without a valid `panel_id`, with a mismatched thread/turn, or from another Telegram message is stale and must not edit/export current run data.
- Pressing `Back` on an older completed run must restore that older Final Card in the same message, not duplicate or replace it with the latest run.
- Finalization sends a new Final Card and moves the panel summary message id to it; Details/Back must edit that new card, not the deleted live commentary card.
- Tool-only turns with no commentary and empty output still expose completed command/status in Details, Tool mode, and Tools file under `Tool activity`.

## Telegram Notification Contract

ADR: `docs/adr/ADR-015-telegram-notification-contract.md`

Primary tests:

- `internal/telegram/api_test.go::TestClientSendMessageSilentSetsDisableNotification`
- `internal/telegram/api_test.go::TestClientSendDocumentSilentSetsDisableNotification`
- `internal/telegram/bot_test.go::TestBotDeliverDirectResponseSendsSilentMessage`
- `internal/config/config_test.go::TestMarshalJSONIncludesNotifyNewRun`
- `tests/config_env_test.go::TestFromEnvDefaultsLoggingOn`
- `tests/config_env_test.go::TestFromEnvPrefersGoScopedEnvVars`
- `internal/daemon/observer_ui_v2_test.go::TestSyncThreadPanelCreatesRouteablePlanPromptAndDedupes`
- `internal/daemon/observer_ui_v2_test.go::TestRunNoticeNotificationFlagCanSilenceNewRun`
- `internal/daemon/observer_ui_v2_test.go::TestFinalTransitionDeletesRunNoticeToolAndOutputButKeepsUser`
- `internal/daemon/observer_ui_v2_test.go::TestFinalCardDetailsCallbacksEditSameMessageAndExportToolsFile`

Live E2E:

- Run a Telegram-origin command with `CTR_GO_NOTIFY_NEW_RUN=on`; verify `New run`, live silent cards, new `[Final]`, and Details/Back.
- Repeat with `CTR_GO_NOTIFY_NEW_RUN=off`; verify `New run` remains visible and `[Final]` still arrives.
- Run a Plan Mode structured-choice prompt and verify `[Plan]` is the only question card with answer buttons.

Contract notes:

- All new bot messages are silent except `New run`, `[Plan]`, and `[Final]`.
- `New run` notification is configurable and enabled by default.
- Explicit exports and direct command/menu responses are silent.
- Old live commentary routes may remain in SQLite after deletion, but active Details routing uses the new Final card message id.

## Turn Lifecycle And Stale Active Recovery

ADR: `docs/adr/ADR-012-turn-lifecycle-normalization.md`

Primary tests:

- `internal/appserver/normalize_test.go::TestSnapshotFromThreadReadTreatsFinalAnswerAsCompletedWhenStatusIsStale`
- `internal/daemon/service_test.go::TestStaleActiveThreadWithFinalAnswerStartsNewTurn`
- `internal/daemon/service_test.go::TestNoActiveTurnSteerFailureFallsBackToTurnStart`
- `internal/daemon/service_test.go::TestReplyToActiveThreadDoesNotFallbackToTurnStartWhenSteerFails`
- `internal/daemon/service_test.go::TestReplyToActiveThreadSteersActiveTurn`
- `internal/daemon/observer_ui_v2_test.go::TestGlobalObserverDoesNotRecreateTelegramOriginPanelOnEditFailure`

Contract notes:

- A final answer is terminal evidence unless the turn is waiting for approval or user input.
- `no active turn to steer` means stale active state and may fall back to a new turn after re-read.
- Active or not-steerable failures still block fallback `turn/start`.
- A Telegram-origin panel must not be duplicated by global observer sync for the same marked turn.

## App Server Session Lifecycle

ADR: `docs/adr/ADR-012-turn-lifecycle-normalization.md`

Primary tests:

- `internal/daemon/service_test.go::TestEnsureLiveSessionSerializedAgainstReconcile`
- `internal/daemon/service_test.go::TestRepairInvalidatesOldLiveLoop`
- `internal/daemon/service_test.go::TestControlLoopProcessesRepairBeforeReconcile`
- `internal/appserver/client_test.go::TestClientStartConcurrentCallsShareInitializedSession`
- `internal/appserver/client_test.go::TestClientStartFailureLeavesClientRetryable`

Contract notes:

- One live App Server session has one live event loop per generation.
- Stale old live-loop closes must not clear newer session state.
- Repair is serialized with reconcile/startup and is processed before replacement reconcile.

## Transient Interrupted Gating

ADR: `docs/adr/ADR-012-turn-lifecycle-normalization.md`

Primary tests:

- `internal/daemon/terminal_gate_test.go::TestTelegramEmptyInterruptedGateDefersAndKeepsHotPollingMetadata`
- `internal/daemon/terminal_gate_test.go::TestTelegramEmptyInterruptedGateRecoversAndClearsDefer`
- `internal/daemon/terminal_gate_test.go::TestTelegramEmptyInterruptedGateGraceExpiryAccepts`
- `internal/daemon/terminal_gate_test.go::TestTelegramEmptyInterruptedGateExplicitInterruptBypassesDefer`
- `internal/daemon/terminal_gate_test.go::TestTelegramFinalInterruptedGateDefersUntilRecovered`
- `internal/daemon/terminal_gate_test.go::TestTelegramPartialInterruptedGateDefersUntilFinalOrGrace`
- `internal/daemon/service_test.go::TestPollTrackedDefersTelegramOriginEmptyInterruptedAndKeepsActiveState`
- `internal/daemon/service_test.go::TestPollTrackedDefersTelegramOriginPartialInterruptedAndKeepsActiveState`
- `internal/daemon/service_test.go::TestPollTrackedDefersTelegramOriginFinalInterruptedAndKeepsActiveState`
- `internal/daemon/service_test.go::TestTelegramOriginHotPollCapturesRunningTool`
- `internal/daemon/service_test.go::TestLiveToolNotificationIgnoresOlderTurnAfterNewerCompletion`
- `internal/daemon/service_test.go::TestRefreshThreadForOperationDefersEmptyInterrupted`

Live E2E:

- checked-in public-safe harness: `tests/live_e2e/telegram_readback_e2e.py`
- requires `CODEX_TG_LIVE_E2E=1`, `CODEX_TG_E2E_THREAD_ID`, a local Telethon session, and bot identity from local env
- uses MTProto readback of edited messages and optional daemon log correlation
- exercises sequential commands plus a multi-command `/reply` math run to catch accidental self-interruption

Contract notes:

- Implicit Telegram-origin `interrupted` is ambiguous until it recovers, expires, or follows explicit `/stop`.
- Deferred terminal state must not collapse the live panel into a false Final Card.
- The daemon must keep polling deferred turns hot.
- Telegram-origin turns get a short App Server `thread/read` hot-poll window after start so `[Tool]` can become visible even when live events do not expose the running command.
- If App Server still has not exposed a tool for an active turn, `[Tool]` must show neutral active-run elapsed time instead of a static empty state.
- Late live tool notifications from older turns must not overwrite a newer completed turn or reintroduce stale `[Tool]` / `[Output]` content.

## Nil-Safe Telegram Rendering

ADR: `docs/adr/ADR-012-turn-lifecycle-normalization.md`

Primary tests:

- `internal/daemon/log_archive_test.go::TestValueFromMapSkipsNilLikeValues`
- `internal/daemon/log_archive_test.go::TestRenderCommandSkipsNilLikeValues`
- `internal/daemon/log_archive_test.go::TestRenderEventMsgWithoutCommandDoesNotPrintNil`
- `internal/daemon/session_tail_overlay_test.go::TestPollTrackedIgnoresStaleSessionTailTool`
- `internal/daemon/observer_ui_v2_test.go::TestSummaryPanelRemovesNilLiteralBeforeRendering`
- `internal/appserver/client_test.go::TestRPCStringSkipsNilLikeValues`
- `internal/appserver/normalize_test.go::TestStringValueTreatsNilLiteralAsMissing`

Live E2E:

- checked-in public-safe harness: `tests/live_e2e/telegram_readback_e2e.py`
- run against a dedicated private test thread from local env, not the working operator thread
- scenarios: sequential `pwd`, `date`, `printf`, dedicated sleep-20 timing, slow command, and multi-command math through `/reply`
- acceptance: scan edited Telegram `[Tool]`, `[Output]`, and `[Final]` messages for literal `"<nil>"`, stale commands from earlier runs, false parallel-turn rejection, and visible non-final `interrupted`

Contract notes:

- Missing App Server fields and literal `"<nil>"` are nil-like values, not display text.
- Telegram rendering must clean nil-like values before Markdown/entity conversion.
- Diagnostics for `telegram_render_contains_nil` are bounded and hash-only.

## Diagnostics And Sanitization

ADR: `docs/adr/ADR-012-turn-lifecycle-normalization.md`

Primary tests:

- `internal/daemon/service_test.go::TestTelegramTurnLifecycleLogsSuccessfulStart`
- `internal/daemon/service_test.go::TestTelegramTurnLifecycleLogsThreadResumeFailure`
- `internal/daemon/service_test.go::TestTelegramTurnLifecycleLogsTurnStartFailure`
- `internal/daemon/service_test.go::TestTelegramTurnLifecycleLogsRefreshFailuresAroundStart`
- `internal/daemon/service_test.go::TestDiagnosticLogsAreRateLimited`
- `internal/daemon/service_test.go::TestDiagnosticLoggerCanBeDisabled`
- `internal/daemon/service_test.go::TestObserverSyncResultLogsAreDebounced`
- `internal/daemon/service_test.go::TestGenericThreadReadDiagnosticsAreDebounced`
- `internal/daemon/service_test.go::TestThreadReadSkippedLogsAreDebounced`
- `internal/telegram/bot_test.go::TestSanitizeTelegramLogErrorRedactsBotTokenURL`
- `tests/config_env_test.go::TestFromEnvDefaultsLoggingOn`
- `tests/config_env_test.go::TestFromEnvInvalidLoggingFlagsFallBackToEnabled`
- `cmd/ctr-go/main_test.go::TestDiagnosticLoggerHonorsFlags`

Contract notes:

- Logs may include ids, route source, operation names, durations, item counts, and sanitized stderr tails.
- Logs must not include full prompt bodies, tokens, session files, SQLite paths, `.env` paths, or unbounded output.
- Diagnostic logging is rate-limited to avoid filesystem floods during app-server loops.
- `CTR_GO_LOG_ENABLED=off` discards daemon stdout logs; `CTR_GO_DIAGNOSTIC_LOGS=off` keeps normal bot logs but suppresses structured lifecycle diagnostics.

## Session Tail Overlay Retirement

ADR: `docs/adr/ADR-013-retire-session-tail-tool-overlay.md`

Feature brief: `docs/process/v0.2.0-live-appserver-events-brief.md`

Primary tests:

- `internal/daemon/session_tail_overlay_test.go::TestPollTrackedIgnoresStaleSessionTailTool`
- `internal/appserver/normalize_test.go::TestToolSnapshotFromLiveNotificationMapsRunningCommand`
- `internal/appserver/normalize_test.go::TestCompactSnapshotStoresToolTimingOnFirstSeen`
- `internal/appserver/normalize_test.go::TestCompactSnapshotPreservesToolTimingWhenUnchanged`
- `internal/appserver/normalize_test.go::TestCompactSnapshotUpdatesToolLastUpdateWhenFingerprintChanges`
- `internal/appserver/normalize_test.go::TestCompactSnapshotDoesNotPreserveActiveLiveToolWhenThreadReadOmitsTool`
- `internal/appserver/normalize_test.go::TestCompactSnapshotDoesNotPreserveLiveToolAcrossTurns`
- `internal/appserver/normalize_test.go::TestSnapshotFromThreadReadKeepsToolOnlyTurnDetailsWithoutCommentary`
- `internal/daemon/service_test.go::TestLiveToolNotificationStoresRunningCommandWithoutRenderingItAsCurrent`
- `internal/daemon/service_test.go::TestPollSnapshotWithoutToolDoesNotPreserveSameTurnRunningToolAsCurrent`
- `internal/daemon/service_test.go::TestPollTrackedDeferredInterruptedDoesNotOverwriteFreshLiveToolSnapshot`
- `internal/daemon/service_test.go::TestPollSnapshotWithOlderCompletedToolPreservesTelegramOriginLiveCurrentTool`
- `internal/daemon/service_test.go::TestRefreshThreadForOperationTerminalCompletedToolReplacesLiveCurrent`
- `internal/daemon/observer_ui_v2_test.go::TestRenderToolPanelShowsLastCompletedToolInsteadOfRunningTool`
- `internal/daemon/observer_ui_v2_test.go::TestRenderToolPanelShowsTelegramOriginCurrentTool`
- `internal/daemon/observer_ui_v2_test.go::TestRenderToolPanelKeepsForeignRunningToolHidden`
- `internal/daemon/observer_ui_v2_test.go::TestRenderSummaryPanelShowsActiveRunElapsedTimeAtBottom`
- `internal/daemon/service_test.go::TestFinalCardShowsRunDuration`

Contract notes:

- App Server `thread/read` snapshots remain the durable source.
- App Server live item notifications may update snapshot/detail history.
- Telegram-origin turns may render current command visibility from live `item/started` and `item/updated` only after matching the marked `thread_id + turn_id`.
- Foreign GUI/CLI runs do not promise authoritative current command visibility.
- Long-running active runs render elapsed runtime in `[commentary]`; completed Final Cards render total `Run duration`.
- `[Tool]` renders the current tool only for eligible Telegram-origin active turns; otherwise it renders the last completed tool, or `No completed tool yet.` when no completed tool is available.
- While a Telegram-origin live current tool is active, older completed tool evidence from same-turn `thread/read` may update `[Output]`, but must not make `[Tool]` revert from the current command to the older completed command.
- Empty/interrupted polling snapshots must not overwrite a fresher stored live current tool for the same Telegram-origin turn.
- `[Output]` renders the last completed tool output when available.
- Session JSONL is not a live Telegram UI source.
- Missing App Server tool state renders as neutral absence, not as a guessed command.
- Session JSONL can still be used for explicit full-log export paths.

Slice gate:

- Each v0.2.0 live-event slice must add or update tests first, pass targeted checks, run the relevant live Telegram E2E case, and only then be committed.

## AFC Passive Group Lifecycle

ADRs: `docs/adr/ADR-020-afc-writer-ownership.md`,
`docs/adr/ADR-021-afc-forum-group-surface.md`, and
`docs/adr/ADR-022-afc-passive-lifecycle.md`

Primary tests:

- `internal/config/config_test.go::TestFromEnvReadsAFCGroupID`
- `internal/storage/store_afc_test.go::TestAFCActivationIsIsolatedAndDisablesObserverOnlyOnSuccess`
- `internal/storage/store_afc_test.go::TestAFCOffIsLogicalBeforeCleanupAndDoesNotRestoreObserver`
- `internal/storage/store_afc_test.go::TestRecoverAFCStateMakesInterruptedActivationCleanupOnly`
- `internal/daemon/afc_test.go::TestAFCPartialActivationOwnsGroupAndDisablesLegacyObserver`
- `internal/daemon/afc_test.go::TestAFCZeroTopicActivationLeavesObserverEnabled`
- `internal/daemon/afc_test.go::TestAFCUnknownTopicAndCallbacksFailClosed`
- `internal/daemon/afc_test.go::TestAFCPassiveSyncSendsSilentStatusAndNotifyingFinal`
- `internal/daemon/afc_test.go::TestAFCPresentationCreatesFreshStatusForEachObservedTurn`
- `internal/daemon/afc_test.go::TestAFCDirectDeliveryReanchorsSameTurnStatusAtTopicTail`
- `internal/daemon/afc_test.go::TestAFCDirectDeliveryKeepsPreviousTurnStatusHistory`
- `internal/daemon/afc_test.go::TestAFCDirectDeliveryDeleteFailureStillCreatesTailStatus`
- `internal/daemon/afc_test.go::TestAFCTopicRenameReanchorsActiveStatusWithoutLosingAggregate`
- `internal/daemon/afc_test.go::TestAFCTopicRenameKeepsPreviousTurnStatusHistory`
- `internal/daemon/afc_test.go::TestAFCControlRepairRequestsSoftSessionRepair`
- `internal/daemon/afc_test.go::TestAFCPresentationIgnoresStalePollTurnWhileAFCWriterIsActive`
- `internal/daemon/afc_test.go::TestAFCStatusAggregatesCommentaryBlocksInOneMessage`
- `internal/daemon/afc_test.go::TestAFCStatusUpdatesSameBlockWithoutDuplicatingAndExcludesTools`
- `internal/daemon/afc_test.go::TestAFCStatusBlockDurationsPartitionOverallDuration`
- `internal/daemon/afc_test.go::TestAFCCompletedStatusCollapsesBodyAndKeepsHeaderVisible`
- `internal/daemon/afc_test.go::TestAFCStatusTrimsOldLinesAndPreservesLatestTail`
- `internal/daemon/afc_test.go::TestAFCPassiveSyncMirrorsDesktopUserBeforeStatusExactlyOnce`
- `internal/daemon/afc_test.go::TestAFCSameTurnDesktopUserReanchorsStatusAfterUser`
- `internal/daemon/afc_test.go::TestAFCTelegramUserIsNotEchoedByPassiveSync`
- `internal/daemon/afc_test.go::TestAFCPendingTelegramUserDefersStaleDesktopSnapshot`
- `internal/storage/store_afc_test.go::TestAFCDispatchPersistsPendingTelegramUserFingerprint`
- `internal/daemon/afc_test.go::TestAFCPassiveSyncTicksElapsedFromStableTurnStart`
- `internal/daemon/afc_test.go::TestAFCPassiveSyncFreezesCompletedDuration`
- `internal/daemon/afc_test.go::TestAFCPassiveSyncRetainsCollapsedAggregateBeforeFinal`
- `internal/daemon/afc_test.go::TestAFCStatusUsesCompactTimingInHeader`
- `internal/appserver/normalize_test.go::TestCompactSnapshotFreezesTurnUpdatedAtAfterTerminalObservation`
- `internal/appserver/normalize_test.go::TestCompactSnapshotDistributesInitiallyObservedStatusBlocks`
- `internal/appserver/normalize_test.go::TestCompactSnapshotDistributesNewStatusBlocksSincePreviousPoll`
- `internal/appserver/normalize_test.go::TestCompactSnapshotPreservesStatusBlockStartWhenTextChanges`
- `internal/appserver/normalize_test.go::TestCompactSnapshotKeepsStatusBlockTimingStableAfterTerminalRestart`
- `internal/daemon/afc_test.go::TestAFCTelegramOriginHotPollRefreshesAndStopsAtTerminal`
- `internal/daemon/afc_test.go::TestAFCLiveToolOverlaySurvivesLaggingThreadReadWithoutEnteringAggregateStatus`
- `internal/daemon/afc_test.go::TestAFCOffMarksOffBeforeCleanupAndDoesNotRestoreLegacy`
- `internal/telegram/bot_test.go::TestBotAFCMessagePreservesRenderedEntitiesOnSendAndEdit`
- `internal/telegram/api_test.go` forum security and typed API failures

Contract notes:

- The exact AFC group is an isolated surface and cannot fall through to legacy handlers.
- Passive AFC performs `thread/list` and `thread/read` only; no writer ownership or App Server mutation is allowed.
- Partial activation is active when at least one topic was persisted; only that transition disables the global observer.
- Logical off precedes cleanup, and off never restores the legacy observer or writer lifecycle.
- Active restart recovery remains passive; interrupted activation becomes cleanup-only.
- AFC status uses the legacy observed turn timing but renders only the compact
  value on its icon-prefixed first line; elapsed-only changes edit the existing
  active-turn status message, while repeated terminal polls preserve the first
  observed end time and do not grow completed duration.
- AFC User, Status, Final, Approval, and Input messages have stable distinct
  icon-prefixed headers. Legacy direct-message presentation is unchanged.
- AFC appends every chronological commentary/reasoning and plan block to one
  status message. A stable item id updates its existing block without resetting
  timing; tools and outputs never enter the aggregate.
- The status first line always carries total state and duration. Approximate
  per-block durations partition that total and persist in the compact snapshot.
  Completed bodies use an expandable quote while the header remains visible;
  Final stays a separate later message.
- Telegram overflow removes oldest body lines, states how many lines were
  removed, preserves the latest block tail, and includes entity layout in the
  delivery fingerprint.
- Desktop-origin user items are mirrored once as silent `[User]` messages;
  same-turn status is reanchored after them. Persisted pending Telegram-input
  fingerprints suppress bot echoes for both new turns and steers, including the
  passive-poll window before ACK delivery.
- A successful prompt/steer acknowledgement is followed by one reanchored live
  status at the topic tail; same-turn stale status is removed best-effort while
  previous-turn history remains intact.
- A successful topic rename reanchors only the same active turn's Status after
  Telegram's rename service message. The replacement is rendered from the
  persisted compact snapshot, preserving every aggregate block and its timing;
  previous-turn and terminal Status history remains intact.
- AFC Control `/repair` queues the same non-destructive App Server session
  repair as legacy DM and does not change AFC lifecycle state.
- Telegram-origin AFC turns use the legacy bounded hot-poll cadence and preserve
  live tool state internally without putting tool/output items into the aggregate
  status or creating legacy panels and bindings.

## AFC Concurrent Managed Turns

ADR: `docs/adr/ADR-023-afc-concurrent-turn-ownership.md`

Primary tests:

- `internal/appserver/writer_manager_test.go::TestWriterManagerForceCloseDropsUnfinishedWorkOnlyAfterProcessClose`
- `internal/storage/store_afc_test.go::TestAFCMessageReceiptIsUniqueAndCarriesNoPromptBody`
- `internal/storage/store_afc_test.go::TestAFCDispatchStateUpdateIsGenerationGuarded`
- `internal/storage/store_afc_test.go::TestRecoverAFCWriterStateMarksUnfinishedInputUnknownWithoutReplay`
- `internal/daemon/afc_test.go::TestAFCConcurrentTopicsShareWriterAndDuplicateDoesNotReplay`
- `internal/daemon/afc_test.go::TestAFCLegacyClaimConflictRejectsBeforeMutation`
- `internal/daemon/afc_test.go::TestAFCOwnershipBlocksLaterLegacyLaunchBeforeMutation`
- `internal/daemon/afc_test.go::TestAFCAmbiguousTurnStartIsUnknownAndNeverReplayed`
- `internal/daemon/afc_test.go::TestAFCTerminalEventsRoutePerTopicAndCloseAfterLastTurn`
- `internal/daemon/afc_test.go::TestAFCPollTerminalEvidenceClosesWriterWhenEventWasMissed`

Contract notes:

- Durable receipts are inserted before mutation and contain no prompt body.
- Duplicate or unknown source messages are never replayed.
- Different topic threads may overlap in one AFC process; one topic has at most one unfinished turn.
- Legacy and AFC share process-level thread claims and reject either conflict before mutation.
- Session/thread/turn/generation guards prevent stale events from crossing topics.
- The process remains open until the last terminal lease and restart creates no writer.

## AFC Guarded Controls And Draining Off

ADR: `docs/adr/ADR-024-afc-guarded-controls-and-draining.md`

Primary tests:

- `internal/daemon/afc_test.go::TestAFCApprovalCallbackIsGuardedByTopicTurnAndGeneration`
- `internal/daemon/afc_test.go::TestAFCDesktopOriginApprovalIsNotActionable`
- `internal/daemon/afc_test.go::TestAFCStructuredUserInputCallbackReturnsGuardedAnswers`
- `internal/daemon/afc_test.go::TestAFCStopInterruptsOnlyCurrentTopicTurn`
- `internal/daemon/afc_test.go::TestAFCSafeOffRefusesActiveTurnsWithoutCleanup`
- `internal/daemon/afc_test.go::TestAFCForceOffInterruptsAllAndWaitsForTerminalBeforeCleanup`
- `internal/daemon/afc_test.go::TestAFCForceOffTimeoutStaysDrainingAndDoesNotCleanup`
- `internal/daemon/afc_test.go::TestAFCRestartUnknownOwnershipBlocksSafeAndForceCleanup`
- `internal/daemon/afc_test.go::TestAFCOffMarksOffBeforeCleanupAndDoesNotRestoreLegacy`

Contract notes:

- AFC callbacks require current session/topic/thread/turn/generation/message coordinates.
- Desktop-origin requests do not create actionable AFC callbacks.
- Stop is topic-local and does not target passive ownership.
- Safe off never cleans up unfinished topics; force off cleans up only after every confirmed terminal.
- Force timeout remains draining, rejects new work, and does not kill the writer.
- Off never restores observer or legacy lifecycle.

## AFC Durable New Task Creation

ADR: `docs/adr/ADR-025-afc-durable-new-task-creation.md`

Primary tests:

- `internal/daemon/afc_test.go::TestAFCProjectPickerCreatesThreadThenTopicThenDurableBinding`
- `internal/daemon/afc_test.go::TestAFCNewTaskDoesNotCreateThreadWhenTopicCreationFails`
- `internal/daemon/afc_test.go::TestAFCExistingEmptyTopicRecoversNoRolloutOnNextPrompt`
- `internal/daemon/afc_test.go::TestAFCExistingEmptyTopicDoesNotRecoverUnrelatedResumeError`
- `internal/daemon/afc_test.go::TestAFCDraftDefinitiveThreadStartFailureCanRetryWithNewMessage`
- `internal/daemon/afc_test.go::TestAFCDraftDefinitiveFirstTurnFailureReturnsTopicToDraft`
- `internal/daemon/afc_test.go::TestAFCOffCleansReadyDraftTopic`
- `internal/daemon/afc_test.go::TestAFCSafeOffRefusesStartingDraft`
- `internal/daemon/afc_test.go::TestAFCDraftRejectsSecondMessageFromCurrentDurableState`
- `internal/daemon/afc_test.go::TestAFCControlHelpListsNewTaskCommands`
- `internal/daemon/afc_test.go::TestAFCPromptTopicTitleKeepsUnicodeAndBoundsLength`
- `internal/daemon/afc_test.go::TestAFCPassiveSyncReconcilesCodexThreadTitleToTopic`
- `internal/storage/store_afc_test.go::TestAFCTopicDraftMaterializesExactlyOnce`
- `internal/storage/store_afc_test.go::TestRecoverAFCWriterMarksStartingDraftUnknown`
- `internal/daemon/afc_test.go::TestAFCProjectsFailClosedWhileOff`
- `internal/daemon/afc_test.go::TestAFCProjectCallbackFromOldSessionFailsBeforeThreadStart`

Contract notes:

- AFC project callbacks carry no first prompt and create only a durable Telegram draft.
- The first draft message performs `thread/start` and first `turn/start` on one writer process before exposing a normal AFC binding.
- A definitive first-turn failure returns the topic to draft state and retains the empty Codex thread.
- Existing empty bindings recover only from precise `no rollout found` evidence and no rendered turn.
- The first prompt supplies the initial Telegram topic and Codex thread name.
- Off-session callbacks fail before App Server mutation.

## Baseline Commands

Run before commit or publish:

```powershell
go test ./...
go build -buildvcs=false ./...
git diff --check
git grep -nE "BOT_TOKEN|TELEGRAM_BOT_TOKEN|api_hash|api_id|phone|password|secret|\\.session|\\.sqlite|\\.env|C:\\\\Users\\\\<private-user>" -- ':!go.sum' ':!.env' ':!.env.example' ':!.git'
```
