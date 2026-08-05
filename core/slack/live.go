package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/RobertWHurst/blackbox"
	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/telemetryos/tos-tag/core/deliveries"
	"github.com/telemetryos/tos-tag/types"
)

const (
	directiveSlashCommand   = "/tag-directive"
	directiveCallbackID     = "tos_tag_channel_directive_v1"
	directivePromptBlockID  = "channel_directive"
	directivePromptActionID = "prompt"
	modeSlashCommand        = "/tag-mode"
	proactiveSlashCommand   = "/tag-proactive"
	assistSlashCommand      = "/tag-assist"
	offSlashCommand         = "/tag-off"
	statusSlashCommand      = "/tag-status"
)

type LiveOptions struct {
	OrganizationID    string
	AppID             string
	TeamID            string
	AppLevelToken     string
	BotUserOAuthToken string
	BotUserID         string
	Logger            *blackbox.Logger
}

type LiveIngress struct {
	options LiveOptions
	client  socketModeTransport
	views   viewAPI
	members membershipAPI

	mu                 sync.Mutex
	cancel             context.CancelFunc
	done               chan struct{}
	started            bool
	interactionHandler ApprovalInteractionHandler
	membershipHandler  BotMembershipHandler
	directiveLoad      DirectiveLoadHandler
	directiveSave      DirectiveSaveHandler
	modeChange         ModeChangeHandler
	reconnectHandler   ReconnectHandler
	recoveryWG         sync.WaitGroup
	stopping           bool
}

type ReconnectHandler func(context.Context) error

func (s *LiveIngress) SetApprovalInteractionHandler(handler ApprovalInteractionHandler) {
	s.mu.Lock()
	s.interactionHandler = handler
	s.mu.Unlock()
}

func (s *LiveIngress) SetBotMembershipHandler(handler BotMembershipHandler) {
	s.mu.Lock()
	s.membershipHandler = handler
	s.mu.Unlock()
}

func (s *LiveIngress) SetDirectiveConfigurationHandlers(load DirectiveLoadHandler, save DirectiveSaveHandler) {
	s.mu.Lock()
	s.directiveLoad = load
	s.directiveSave = save
	s.mu.Unlock()
}

func (s *LiveIngress) SetModeChangeHandler(handler ModeChangeHandler) {
	s.mu.Lock()
	s.modeChange = handler
	s.mu.Unlock()
}

func (s *LiveIngress) SetReconnectHandler(handler ReconnectHandler) {
	s.mu.Lock()
	s.reconnectHandler = handler
	s.mu.Unlock()
}

type socketModeTransport interface {
	RunContext(context.Context) error
	AckCtx(context.Context, string, any) error
	EventsChannel() <-chan socketmode.Event
}

type managedSocketModeTransport struct {
	client *socketmode.Client
}

type viewAPI interface {
	OpenViewContext(context.Context, string, slackapi.ModalViewRequest) (*slackapi.ViewResponse, error)
}

type membershipAPI interface {
	GetConversationInfoContext(context.Context, *slackapi.GetConversationInfoInput) (*slackapi.Channel, error)
	JoinConversationContext(context.Context, string) (*slackapi.Channel, string, []string, error)
	LeaveConversationContext(context.Context, string) (bool, error)
}

func (t managedSocketModeTransport) RunContext(ctx context.Context) error {
	return t.client.RunContext(ctx)
}

func (t managedSocketModeTransport) AckCtx(ctx context.Context, envelopeID string, payload any) error {
	return t.client.AckCtx(ctx, envelopeID, payload)
}

func (t managedSocketModeTransport) EventsChannel() <-chan socketmode.Event {
	return t.client.Events
}

type deliveryAPI interface {
	PostMessageContext(context.Context, string, ...slackapi.MsgOption) (string, string, error)
	UpdateMessageContext(context.Context, string, string, ...slackapi.MsgOption) (string, string, string, error)
	SetAssistantThreadsStatusContext(context.Context, slackapi.AssistantThreadsSetStatusParameters) error
	StartStreamContext(context.Context, string, ...slackapi.MsgOption) (string, string, error)
	AppendStreamContext(context.Context, string, string, ...slackapi.MsgOption) (string, string, error)
	StopStreamContext(context.Context, string, string, ...slackapi.MsgOption) (string, string, error)
	AddReactionContext(context.Context, string, slackapi.ItemRef) error
	GetConversationHistoryContext(context.Context, *slackapi.GetConversationHistoryParameters) (*slackapi.GetConversationHistoryResponse, error)
	GetConversationRepliesContext(context.Context, *slackapi.GetConversationRepliesParameters) ([]slackapi.Message, bool, string, error)
}

type LiveDelivery struct {
	teamID   string
	api      deliveryAPI
	renderer *deliveries.Renderer
	logger   *blackbox.Logger
}

func (d *LiveDelivery) StartProgress(ctx context.Context, request types.SlackProgressStartRequest) (types.SlackProgressResult, error) {
	started := time.Now()
	logger := d.logger
	if logger == nil {
		logger = blackbox.New()
	}
	requestLogger := logger.WithCtx(blackbox.Ctx{"job_id": request.JobID, "channel_id": request.ChannelID, "thread_ts": request.ThreadTS, "progress_step_id": request.Step.ID})
	requestLogger.Info("Slack Thinking Steps start requested")
	if request.TeamID != d.teamID {
		return types.SlackProgressResult{}, errors.New("Slack progress team does not match the configured installation")
	}
	if request.IdempotencyKey == "" || request.ChannelID == "" || request.ThreadTS == "" || request.JobID == "" || request.RecipientUserID == "" || request.Title == "" {
		return types.SlackProgressResult{}, errors.New("invalid Slack progress start request")
	}
	chunk, err := slackTaskUpdate(request.Step)
	if err != nil {
		return types.SlackProgressResult{}, err
	}
	if timestamp, findErr := d.progressMessage(ctx, request.ChannelID, request.ThreadTS, request.IdempotencyKey); findErr != nil {
		return types.SlackProgressResult{}, fmt.Errorf("reconcile Slack progress: %w", findErr)
	} else if timestamp != "" {
		requestLogger.WithCtx(blackbox.Ctx{"message_ts": timestamp, "duration_ms": time.Since(started).Milliseconds(), "duplicate": true}).Info("Slack Thinking Steps reconciled")
		return types.SlackProgressResult{MessageTS: timestamp, UpdatedAt: time.Now().UTC(), Duplicate: true}, nil
	}
	// Use Slack's native transient agent status to bridge the handoff from the
	// source-message acknowledgement reaction into the structured progress
	// stream. This avoids exposing a literal message-like "Thinking..." state;
	// failure is cosmetic and must not prevent the answer or its safe progress.
	if err := d.api.SetAssistantThreadsStatusContext(ctx, slackapi.AssistantThreadsSetStatusParameters{
		ChannelID: request.ChannelID,
		ThreadTS:  request.ThreadTS,
		Status:    "Organizing…",
	}); err != nil {
		requestLogger.WithCtx(blackbox.Ctx{"error_type": fmt.Sprintf("%T", err), "slack_error_code": slackAPIErrorCode(err)}).Warn("Slack agent status failed; continuing with progress stream")
	} else {
		requestLogger.Info("Slack agent status set")
	}
	options := []slackapi.MsgOption{
		slackapi.MsgOptionRecipientTeamID(request.TeamID),
		slackapi.MsgOptionRecipientUserID(request.RecipientUserID),
		slackapi.MsgOptionTaskDisplayMode(slackapi.TaskDisplayModePlan),
		slackapi.MsgOptionChunks(slackapi.NewPlanUpdateChunk(request.Title), chunk),
		slackapi.MsgOptionMetadata(slackapi.SlackMetadata{EventType: "tos_tag_progress", EventPayload: map[string]any{"job_id": string(request.JobID), "idempotency_key": request.IdempotencyKey}}),
	}
	options = append(options, slackapi.MsgOptionTS(request.ThreadTS))
	_, timestamp, err := d.api.StartStreamContext(ctx, request.ChannelID, options...)
	if err != nil {
		requestLogger.WithCtx(blackbox.Ctx{"duration_ms": time.Since(started).Milliseconds(), "error_type": fmt.Sprintf("%T", err), "slack_error_code": slackAPIErrorCode(err)}).Error("Slack Thinking Steps start failed")
		return types.SlackProgressResult{}, err
	}
	requestLogger.WithCtx(blackbox.Ctx{"message_ts": timestamp, "duration_ms": time.Since(started).Milliseconds()}).Info("Slack Thinking Steps started")
	return types.SlackProgressResult{MessageTS: timestamp, UpdatedAt: time.Now().UTC()}, nil
}

func (d *LiveDelivery) UpdateProgress(ctx context.Context, request types.SlackProgressUpdateRequest) (types.SlackProgressResult, error) {
	started := time.Now()
	logger := d.logger
	if logger == nil {
		logger = blackbox.New()
	}
	requestLogger := logger.WithCtx(blackbox.Ctx{"job_id": request.JobID, "channel_id": request.ChannelID, "message_ts": request.MessageTS, "progress_step_id": request.Step.ID, "progress_status": request.Step.Status})
	if request.TeamID != d.teamID {
		return types.SlackProgressResult{}, errors.New("Slack progress team does not match the configured installation")
	}
	if request.ChannelID == "" || request.MessageTS == "" || request.JobID == "" {
		return types.SlackProgressResult{}, errors.New("invalid Slack progress update request")
	}
	chunk, err := slackTaskUpdate(request.Step)
	if err != nil {
		return types.SlackProgressResult{}, err
	}
	_, timestamp, err := d.api.AppendStreamContext(ctx, request.ChannelID, request.MessageTS, slackapi.MsgOptionChunks(chunk))
	if err != nil {
		requestLogger.WithCtx(blackbox.Ctx{"duration_ms": time.Since(started).Milliseconds(), "error_type": fmt.Sprintf("%T", err), "slack_error_code": slackAPIErrorCode(err)}).Error("Slack Thinking Steps update failed")
		return types.SlackProgressResult{}, err
	}
	if timestamp == "" {
		timestamp = request.MessageTS
	}
	requestLogger.WithCtx(blackbox.Ctx{"duration_ms": time.Since(started).Milliseconds()}).Info("Slack Thinking Steps updated")
	return types.SlackProgressResult{MessageTS: timestamp, UpdatedAt: time.Now().UTC()}, nil
}

func slackTaskUpdate(step types.SlackProgressStep) (slackapi.TaskUpdateChunk, error) {
	if step.ID == "" || step.Title == "" {
		return slackapi.TaskUpdateChunk{}, errors.New("Slack progress step requires id and title")
	}
	status := slackapi.TaskCardStatus(step.Status)
	switch status {
	case slackapi.TaskCardStatusPending, slackapi.TaskCardStatusInProgress, slackapi.TaskCardStatusComplete, slackapi.TaskCardStatusError:
	default:
		return slackapi.TaskUpdateChunk{}, errors.New("invalid Slack progress status")
	}
	chunk := slackapi.NewTaskUpdateChunk(step.ID, step.Title)
	chunk.Status, chunk.Details, chunk.Output = status, step.Details, step.Output
	for _, source := range step.Sources {
		parsed, err := url.Parse(source.URL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || source.Text == "" {
			return slackapi.TaskUpdateChunk{}, errors.New("invalid Slack progress source")
		}
		chunk.Sources = append(chunk.Sources, slackapi.NewTaskCardSource(source.URL, source.Text))
	}
	return chunk, nil
}

// NewLive constructs the production adapters without opening a connection.
// Start is the only method that initiates Socket Mode network activity.
func NewLive(options LiveOptions, renderer *deliveries.Renderer) (*LiveIngress, *LiveDelivery, error) {
	if options.OrganizationID == "" || !strings.HasPrefix(options.AppID, "A") || options.TeamID == "" || !strings.HasPrefix(options.AppLevelToken, "xapp-") || !strings.HasPrefix(options.BotUserOAuthToken, "xoxb-") {
		return nil, nil, errors.New("invalid live Slack options")
	}
	if renderer == nil {
		return nil, nil, errors.New("Slack renderer is required")
	}
	api := slackapi.New(options.BotUserOAuthToken, slackapi.OptionAppLevelToken(options.AppLevelToken))
	client := managedSocketModeTransport{client: socketmode.New(api)}
	if options.Logger == nil {
		options.Logger = blackbox.New()
	}
	options.Logger = options.Logger.WithCtx(blackbox.Ctx{"component": "slack", "slack_app_id": options.AppID, "slack_team_id": options.TeamID})
	return &LiveIngress{options: options, client: client, views: api, members: api}, &LiveDelivery{teamID: options.TeamID, api: api, renderer: renderer, logger: options.Logger}, nil
}

func (s *LiveIngress) Start(parent context.Context, handler Handler) error {
	if handler == nil {
		return errors.New("Slack handler is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return nil
	}
	if s.stopping {
		return errors.New("Slack ingress is stopping")
	}
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.done = make(chan struct{})
	s.started = true
	go s.run(ctx, handler)
	return nil
}

func (s *LiveIngress) run(ctx context.Context, handler Handler) {
	defer close(s.done)
	s.options.Logger.Info("Slack Socket Mode loop started")
	defer s.options.Logger.Info("Slack Socket Mode loop stopped")
	runDone := make(chan error, 1)
	go func() { runDone <- s.client.RunContext(ctx) }()
	connectedOnce := false
	for {
		select {
		case <-ctx.Done():
			<-runDone
			return
		case err := <-runDone:
			if ctx.Err() == nil {
				s.options.Logger.WithCtx(blackbox.Ctx{"error_type": fmt.Sprintf("%T", err)}).Error("Slack Socket Mode client exited unexpectedly")
			}
			return
		case event, open := <-s.client.EventsChannel():
			if !open {
				s.options.Logger.Warn("Slack Socket Mode event stream closed")
				return
			}
			requestContext := blackbox.Ctx{"socket_event_type": string(event.Type)}
			if event.Request != nil {
				requestContext["envelope_id"] = event.Request.EnvelopeID
				requestContext["retry_attempt"] = event.Request.RetryAttempt
				requestContext["retry_reason"] = event.Request.RetryReason
			}
			eventLogger := s.options.Logger.WithCtx(requestContext)
			switch event.Type {
			case socketmode.EventTypeConnecting:
				eventLogger.Info("Slack Socket Mode connecting")
				continue
			case socketmode.EventTypeConnected:
				connectionCount := 0
				if connected, ok := event.Data.(*socketmode.ConnectedEvent); ok {
					connectionCount = connected.ConnectionCount
				}
				reconnected := connectedOnce
				connectedOnce = true
				eventLogger.WithCtx(blackbox.Ctx{"connection_count": connectionCount, "reconnected": reconnected}).Info("Slack Socket Mode transport connected")
				if reconnected {
					s.mu.Lock()
					reconnectHandler := s.reconnectHandler
					launchRecovery := reconnectHandler != nil && !s.stopping
					if launchRecovery {
						s.recoveryWG.Add(1)
					}
					s.mu.Unlock()
					if launchRecovery {
						go func() {
							defer s.recoveryWG.Done()
							if err := reconnectHandler(ctx); err != nil && ctx.Err() == nil {
								s.options.Logger.WithCtx(blackbox.Ctx{"error_type": fmt.Sprintf("%T", err)}).Warn("Slack reconnect context recovery failed")
								return
							}
							if ctx.Err() == nil {
								s.options.Logger.Info("Slack reconnect context recovery completed")
							}
						}()
					}
				}
				continue
			case socketmode.EventTypeHello:
				if event.Request == nil {
					eventLogger.Error("Slack Socket Mode hello missing request metadata")
					continue
				}
				actualAppID := event.Request.ConnectionInfo.AppID
				if actualAppID != s.options.AppID {
					eventLogger.WithCtx(blackbox.Ctx{"actual_slack_app_id": actualAppID}).Error("Slack App-Level Token belongs to a different app")
					s.cancel()
					continue
				}
				eventLogger.Info("Slack Socket Mode hello verified")
				continue
			case socketmode.EventTypeInvalidAuth, socketmode.EventTypeConnectionError, socketmode.EventTypeIncomingError, socketmode.EventTypeErrorWriteFailed, socketmode.EventTypeErrorBadMessage:
				eventLogger.WithCtx(socketModeErrorContext(event.Data)).Error("Slack Socket Mode transport error")
				continue
			case socketmode.EventTypeEventsAPI:
				eventLogger.Info("Slack Events API envelope received")
			case socketmode.EventTypeSlashCommand:
				if event.Request == nil {
					eventLogger.Error("Slack slash command missing request metadata")
					continue
				}
				modeRequest, modeEligible, modeErr := NormalizeModeCommand(s.options, event.Data)
				if modeErr != nil {
					eventLogger.WithCtx(blackbox.Ctx{"error_type": fmt.Sprintf("%T", modeErr)}).Error("Slack mode command rejected")
					_ = s.client.AckCtx(ctx, event.Request.EnvelopeID, ephemeralCommandResponse("This command is not available in this workspace or channel."))
					continue
				}
				if modeEligible {
					s.handleModeCommand(ctx, eventLogger, event.Request.EnvelopeID, modeRequest)
					continue
				}
				statusRequest, statusEligible, statusErr := NormalizeStatusCommand(s.options, event.Data)
				if statusErr != nil {
					eventLogger.WithCtx(blackbox.Ctx{"error_type": fmt.Sprintf("%T", statusErr)}).Error("Slack status command rejected")
					_ = s.client.AckCtx(ctx, event.Request.EnvelopeID, ephemeralCommandResponse("Usage: `/tag-status` — run it without arguments in the channel you want to inspect."))
					continue
				}
				if statusEligible {
					s.handleStatusCommand(ctx, eventLogger, event.Request.EnvelopeID, statusRequest)
					continue
				}
				command, eligible, normalizeErr := NormalizeDirectiveCommand(s.options, event.Data)
				if normalizeErr != nil {
					eventLogger.WithCtx(blackbox.Ctx{"error_type": fmt.Sprintf("%T", normalizeErr)}).Error("Slack directive command rejected")
					_ = s.client.AckCtx(ctx, event.Request.EnvelopeID, ephemeralCommandResponse("This command is not available in this workspace or channel."))
					continue
				}
				if !eligible {
					eventLogger.Debug("Slack slash command ignored")
					_ = s.client.AckCtx(ctx, event.Request.EnvelopeID, nil)
					continue
				}
				s.mu.Lock()
				loadDirective := s.directiveLoad
				s.mu.Unlock()
				if loadDirective == nil || s.views == nil {
					eventLogger.Error("Slack directive command has no configuration handler")
					_ = s.client.AckCtx(ctx, event.Request.EnvelopeID, ephemeralCommandResponse("Channel directives are not available right now."))
					continue
				}
				configuration, loadErr := loadDirective(ctx, command.Request)
				if loadErr != nil {
					eventLogger.WithCtx(blackbox.Ctx{"channel_id": command.Request.ChannelID, "actor_id": command.Request.UserID, "error_type": fmt.Sprintf("%T", loadErr)}).Warn("Slack directive command scope check failed")
					_ = s.client.AckCtx(ctx, event.Request.EnvelopeID, ephemeralCommandResponse("Channel directives are not available in this channel right now."))
					continue
				}
				if ackErr := s.client.AckCtx(ctx, event.Request.EnvelopeID, nil); ackErr != nil {
					eventLogger.WithCtx(blackbox.Ctx{"error_type": fmt.Sprintf("%T", ackErr)}).Error("Slack directive command acknowledgement failed")
					continue
				}
				if _, openErr := s.views.OpenViewContext(ctx, command.TriggerID, directiveModal(command.Request, configuration)); openErr != nil {
					eventLogger.WithCtx(blackbox.Ctx{"channel_id": command.Request.ChannelID, "actor_id": command.Request.UserID, "error_type": fmt.Sprintf("%T", openErr)}).Error("Slack directive modal open failed")
					continue
				}
				eventLogger.WithCtx(blackbox.Ctx{"channel_id": command.Request.ChannelID, "actor_id": command.Request.UserID, "active_revision": configuration.Revision}).Info("Slack directive modal opened")
				continue
			case socketmode.EventTypeInteractive:
				if event.Request == nil {
					eventLogger.Error("Slack interaction missing request metadata")
					continue
				}
				directive, directiveEligible, directiveErr := NormalizeDirectiveSubmission(s.options, event.Data)
				if directiveErr != nil {
					eventLogger.WithCtx(blackbox.Ctx{"error_type": fmt.Sprintf("%T", directiveErr)}).Error("Slack directive submission rejected")
					_ = s.client.AckCtx(ctx, event.Request.EnvelopeID, slackapi.NewErrorsViewSubmissionResponse(map[string]string{directivePromptBlockID: "The directive could not be validated. Reopen the form and try again."}))
					continue
				}
				if directiveEligible {
					s.mu.Lock()
					saveDirective := s.directiveSave
					s.mu.Unlock()
					if saveDirective == nil {
						eventLogger.Error("Slack directive submission has no durable handler")
						_ = s.client.AckCtx(ctx, event.Request.EnvelopeID, slackapi.NewErrorsViewSubmissionResponse(map[string]string{directivePromptBlockID: "Channel directives are unavailable right now."}))
						continue
					}
					saved, saveErr := saveDirective(ctx, directive)
					if saveErr != nil {
						eventLogger.WithCtx(blackbox.Ctx{"channel_id": directive.ChannelID, "actor_id": directive.UserID, "error_type": fmt.Sprintf("%T", saveErr)}).Error("Slack directive submission persistence failed")
						_ = s.client.AckCtx(ctx, event.Request.EnvelopeID, slackapi.NewErrorsViewSubmissionResponse(map[string]string{directivePromptBlockID: "The directive was not saved. Check that the channel is still available and try again."}))
						continue
					}
					if ackErr := s.client.AckCtx(ctx, event.Request.EnvelopeID, nil); ackErr != nil {
						eventLogger.WithCtx(blackbox.Ctx{"channel_id": directive.ChannelID, "directive_revision": saved.Revision, "error_type": fmt.Sprintf("%T", ackErr)}).Error("Slack directive submission acknowledgement failed")
						continue
					}
					eventLogger.WithCtx(blackbox.Ctx{"channel_id": directive.ChannelID, "actor_id": directive.UserID, "directive_revision": saved.Revision, "prompt_bytes": len(saved.Prompt)}).Info("Slack channel directive saved and activated")
					continue
				}
				interaction, eligible, normalizeErr := NormalizeApprovalInteraction(s.options, event.Data)
				if normalizeErr != nil {
					eventLogger.WithCtx(blackbox.Ctx{"error_type": fmt.Sprintf("%T", normalizeErr)}).Error("Slack approval interaction rejected")
					continue
				}
				if !eligible {
					eventLogger.Debug("Slack interaction ignored")
					continue
				}
				s.mu.Lock()
				interactionHandler := s.interactionHandler
				s.mu.Unlock()
				if interactionHandler == nil {
					eventLogger.Error("Slack approval interaction has no durable handler")
					continue
				}
				if handleErr := interactionHandler(ctx, interaction); handleErr != nil {
					eventLogger.WithCtx(blackbox.Ctx{"approval_id": interaction.ApprovalID, "approver_id": interaction.UserID, "diagnostic_code": approvalInteractionDiagnosticCode(handleErr), "error_type": fmt.Sprintf("%T", handleErr)}).Error("Slack approval interaction persistence failed; acknowledgement withheld")
					continue
				}
				if ackErr := s.client.AckCtx(ctx, event.Request.EnvelopeID, nil); ackErr != nil {
					eventLogger.WithCtx(blackbox.Ctx{"approval_id": interaction.ApprovalID, "error_type": fmt.Sprintf("%T", ackErr)}).Error("Slack approval interaction acknowledgement failed")
					continue
				}
				eventLogger.WithCtx(blackbox.Ctx{"approval_id": interaction.ApprovalID, "approver_id": interaction.UserID, "approved": interaction.Approve}).Info("Slack approval interaction durably handled and acknowledged")
				continue
			default:
				eventLogger.Debug("Slack Socket Mode event ignored by Events API ingress")
				continue
			}
			if event.Request == nil {
				eventLogger.Error("Slack Events API envelope missing request metadata")
				continue
			}
			membership, membershipEligible, membershipErr := NormalizeBotMembershipChange(s.options.OrganizationID, s.options.BotUserID, event.Data)
			if membershipErr != nil {
				eventLogger.WithCtx(blackbox.Ctx{"error_type": fmt.Sprintf("%T", membershipErr)}).Error("Slack bot membership event rejected")
				continue
			}
			if membershipEligible {
				s.mu.Lock()
				membershipHandler := s.membershipHandler
				s.mu.Unlock()
				if membershipHandler == nil {
					eventLogger.Error("Slack bot membership event has no policy handler")
					continue
				}
				if handleErr := membershipHandler(ctx, membership); handleErr != nil {
					eventLogger.WithCtx(blackbox.Ctx{"channel_id": membership.ChannelID, "joined": membership.Joined, "error_type": fmt.Sprintf("%T", handleErr)}).Error("Slack bot membership policy reconciliation failed; acknowledgement withheld")
					continue
				}
				if ackErr := s.client.AckCtx(ctx, event.Request.EnvelopeID, nil); ackErr != nil {
					eventLogger.WithCtx(blackbox.Ctx{"channel_id": membership.ChannelID, "joined": membership.Joined, "error_type": fmt.Sprintf("%T", ackErr)}).Error("Slack bot membership event acknowledgement failed")
					continue
				}
				eventLogger.WithCtx(blackbox.Ctx{"channel_id": membership.ChannelID, "joined": membership.Joined}).Info("Slack bot membership policy reconciled and acknowledged")
				continue
			}
			started := time.Now()
			normalized, eligible, err := NormalizeEventsAPI(s.options.OrganizationID, s.options.BotUserID, event.Request.EnvelopeID, event.Data)
			if err != nil {
				eventLogger.WithCtx(blackbox.Ctx{"duration_ms": time.Since(started).Milliseconds(), "error_type": fmt.Sprintf("%T", err)}).Error("Slack Events API normalization failed")
				continue
			}
			if !eligible {
				if ackErr := s.client.AckCtx(ctx, event.Request.EnvelopeID, nil); ackErr != nil {
					eventLogger.WithCtx(blackbox.Ctx{"error_type": fmt.Sprintf("%T", ackErr)}).Error("Slack ineligible envelope acknowledgement failed")
				} else {
					eventLogger.WithCtx(blackbox.Ctx{"duration_ms": time.Since(started).Milliseconds()}).Info("Slack ineligible envelope acknowledged")
				}
				continue
			}
			envelopeLogger := eventLogger.WithCtx(slackEnvelopeLogContext(normalized))
			envelopeLogger.Info("Slack event normalized")
			accepted, err := handler(ctx, normalized)
			if err != nil {
				// Deliberately do not acknowledge: Slack may retry after a
				// persistence or admission failure.
				envelopeLogger.WithCtx(blackbox.Ctx{"duration_ms": time.Since(started).Milliseconds(), "error_type": fmt.Sprintf("%T", err)}).Error("Slack event rejected; acknowledgement withheld")
				continue
			}
			if ackErr := s.client.AckCtx(ctx, event.Request.EnvelopeID, nil); ackErr != nil {
				envelopeLogger.WithCtx(blackbox.Ctx{"duration_ms": time.Since(started).Milliseconds(), "duplicate": accepted.Duplicate, "ignored": accepted.Ignored, "error_type": fmt.Sprintf("%T", ackErr)}).Error("Slack event acknowledgement failed")
				continue
			}
			if accepted.Ignored {
				if accepted.ResolvedContext {
					envelopeLogger.WithCtx(blackbox.Ctx{"duration_ms": time.Since(started).Milliseconds(), "ignored": true, "resolved_context": true}).Debug("Slack self-authored context acknowledged without decision admission")
				} else {
					envelopeLogger.WithCtx(blackbox.Ctx{"duration_ms": time.Since(started).Milliseconds(), "ignored": true}).Info("Slack policy-excluded envelope acknowledged without persistence")
				}
				continue
			}
			envelopeLogger.WithCtx(blackbox.Ctx{"duration_ms": time.Since(started).Milliseconds(), "duplicate": accepted.Duplicate, "ignored": false}).Info("Slack event durably accepted and acknowledged")
		}
	}
}

func socketModeErrorContext(data any) blackbox.Ctx {
	ctx := blackbox.Ctx{"error_type": fmt.Sprintf("%T", data)}
	if socketErr, ok := data.(error); ok {
		ctx["error"] = socketErr.Error()
	}
	return ctx
}

func NormalizeBotMembershipChange(organizationID, botUserID string, data any) (BotMembershipChange, bool, error) {
	event, ok := data.(slackevents.EventsAPIEvent)
	if !ok || event.Type != slackevents.CallbackEvent {
		return BotMembershipChange{}, false, nil
	}
	var callback *slackevents.EventsAPICallbackEvent
	switch value := event.Data.(type) {
	case *slackevents.EventsAPICallbackEvent:
		callback = value
	case slackevents.EventsAPICallbackEvent:
		callback = &value
	}
	if callback == nil {
		return BotMembershipChange{}, false, errors.New("Slack callback payload is malformed")
	}
	change := BotMembershipChange{OrganizationID: organizationID, WorkspaceID: callback.TeamID, EventID: callback.EventID}
	switch inner := event.InnerEvent.Data.(type) {
	case *slackevents.MemberJoinedChannelEvent:
		if inner.User != botUserID {
			return BotMembershipChange{}, false, nil
		}
		change.ChannelID, change.Joined = inner.Channel, true
	case *slackevents.MemberLeftChannelEvent:
		if inner.User != botUserID {
			return BotMembershipChange{}, false, nil
		}
		change.ChannelID, change.Joined = inner.Channel, false
	default:
		return BotMembershipChange{}, false, nil
	}
	if botUserID == "" || change.WorkspaceID == "" || change.ChannelID == "" {
		return BotMembershipChange{}, false, errors.New("Slack bot membership event is incomplete")
	}
	return change, true, nil
}

func approvalInteractionDiagnosticCode(err error) string {
	if err == nil {
		return "none"
	}
	message := strings.ToLower(err.Error())
	for fragment, code := range map[string]string{
		"decision is incomplete":        "incomplete_decision",
		"destination does not match":    "destination_mismatch",
		"approver is not authorized":    "approver_not_authorized",
		"independent approver required": "independent_approver_required",
		"load approval job":             "job_load_failed",
		"job does not match":            "job_scope_mismatch",
		"record slack approval":         "audit_write_failed",
		"not approvable":                "approval_state_conflict",
		"not deniable":                  "approval_state_conflict",
		"resume approved job":           "job_resume_failed",
		"cancel denied job":             "job_cancel_failed",
		"enqueue":                       "delivery_enqueue_failed",
	} {
		if strings.Contains(message, fragment) {
			return code
		}
	}
	return "persistence_failed"
}

func (s *LiveIngress) handleModeCommand(ctx context.Context, eventLogger *blackbox.Logger, envelopeID string, request ModeChangeRequest) {
	commandLogger := eventLogger.WithCtx(blackbox.Ctx{"channel_id": request.ChannelID, "actor_id": request.UserID, "command": request.Command, "requested_mode": request.Mode})
	switch request.Mode {
	case "", "observe", "assist", "proactive":
	default:
		_ = s.client.AckCtx(ctx, envelopeID, ephemeralCommandResponse("Usage: `/tag-mode observe | assist | proactive` — or run it with no argument to see the current mode."))
		return
	}
	s.mu.Lock()
	changeMode := s.modeChange
	s.mu.Unlock()
	if changeMode == nil {
		commandLogger.Error("Slack mode command has no configuration handler")
		_ = s.client.AckCtx(ctx, envelopeID, ephemeralCommandResponse("Participation mode changes are not available right now."))
		return
	}
	fixedCommand := request.Command != "" && request.Command != modeSlashCommand
	var policy ModeChangeResult
	if fixedCommand {
		preflight := request
		preflight.Mode = ""
		var inspectErr error
		policy, inspectErr = changeMode(ctx, preflight)
		if inspectErr != nil {
			commandLogger.WithCtx(blackbox.Ctx{"error_type": fmt.Sprintf("%T", inspectErr)}).Warn("Slack mode command scope check failed")
			_ = s.client.AckCtx(ctx, envelopeID, ephemeralCommandResponse("Tag's participation level is not configurable in this channel right now."))
			return
		}
		if request.Mode != "observe" && (!policy.Enrolled || policy.KillSwitched || !policy.WorkspaceEnabled) {
			_ = s.client.AckCtx(ctx, envelopeID, ephemeralCommandResponse("Tag cannot be activated here because this channel or workspace is not enabled."))
			return
		}
	}

	result, changeErr := changeMode(ctx, request)
	if changeErr != nil {
		commandLogger.WithCtx(blackbox.Ctx{"error_type": fmt.Sprintf("%T", changeErr)}).Warn("Slack mode command failed")
		_ = s.client.AckCtx(ctx, envelopeID, ephemeralCommandResponse("The participation mode could not be changed for this channel."))
		return
	}
	var membership modeMembershipResult
	if fixedCommand && request.Mode != "observe" {
		membership = s.ensureJoinedForMode(ctx, commandLogger, request, policy)
	}
	if fixedCommand && request.Mode == "observe" {
		membership = s.leaveAfterModeOff(ctx, commandLogger, request, policy)
	}
	if membership.changed {
		s.recordCommandMembership(ctx, commandLogger, request, membership.joined)
	}
	response := "Tag is in *" + result.Mode + "* mode in this channel. Use `/tag-mode observe | assist | proactive` to change it."
	if result.Changed {
		response = "Tag participation mode for this channel is now *" + result.Mode + "* (was " + result.Previous + ")."
	} else if request.Mode != "" {
		response = "Tag is already in *" + result.Mode + "* mode in this channel."
	}
	if fixedCommand {
		level := "*" + result.Mode + "*"
		if request.Mode == "observe" {
			level = "*off* (`observe`)"
		}
		response = "Tag's participation level for this channel is now " + level + "."
		if membership.message != "" {
			response += " " + membership.message
		}
	}
	if ackErr := s.client.AckCtx(ctx, envelopeID, ephemeralCommandResponse(response)); ackErr != nil {
		commandLogger.WithCtx(blackbox.Ctx{"error_type": fmt.Sprintf("%T", ackErr)}).Error("Slack mode command acknowledgement failed")
		return
	}
	commandLogger.WithCtx(blackbox.Ctx{"effective_mode": result.Mode, "changed": result.Changed}).Info("Slack mode command handled")
}

func (s *LiveIngress) handleStatusCommand(ctx context.Context, eventLogger *blackbox.Logger, envelopeID string, request ModeChangeRequest) {
	commandLogger := eventLogger.WithCtx(blackbox.Ctx{"channel_id": request.ChannelID, "actor_id": request.UserID, "command": request.Command})
	s.mu.Lock()
	loadMode := s.modeChange
	loadDirective := s.directiveLoad
	s.mu.Unlock()
	if loadMode == nil {
		commandLogger.Error("Slack status command has no configuration handler")
		_ = s.client.AckCtx(ctx, envelopeID, ephemeralCommandResponse("Tag's channel status is not available right now."))
		return
	}
	policy, policyErr := loadMode(ctx, request)
	if policyErr != nil {
		commandLogger.WithCtx(blackbox.Ctx{"error_type": fmt.Sprintf("%T", policyErr)}).Warn("Slack status command policy lookup failed")
		_ = s.client.AckCtx(ctx, envelopeID, ephemeralCommandResponse("Tag's channel status is not available in this channel right now."))
		return
	}

	directive := DirectiveConfiguration{}
	directiveAvailable := loadDirective != nil
	if loadDirective != nil {
		var directiveErr error
		directive, directiveErr = loadDirective(ctx, DirectiveConfigurationRequest{
			OrganizationID: request.OrganizationID,
			WorkspaceID:    request.WorkspaceID,
			ChannelID:      request.ChannelID,
			UserID:         request.UserID,
		})
		if directiveErr != nil {
			directiveAvailable = false
			commandLogger.WithCtx(blackbox.Ctx{"error_type": fmt.Sprintf("%T", directiveErr)}).Warn("Slack status command directive lookup failed")
		}
	}
	if ackErr := s.client.AckCtx(ctx, envelopeID, ephemeralStatusResponse(policy, directive, directiveAvailable)); ackErr != nil {
		commandLogger.WithCtx(blackbox.Ctx{"error_type": fmt.Sprintf("%T", ackErr)}).Error("Slack status command acknowledgement failed")
		return
	}
	commandLogger.WithCtx(blackbox.Ctx{"effective_mode": policy.Mode, "directive_revision": directive.Revision, "directive_available": directiveAvailable}).Info("Slack status command handled")
}

type modeMembershipResult struct {
	changed bool
	joined  bool
	message string
}

func (s *LiveIngress) ensureJoinedForMode(ctx context.Context, logger *blackbox.Logger, request ModeChangeRequest, policy ModeChangeResult) modeMembershipResult {
	if policy.BotMembershipKnown && policy.BotIsMember {
		return modeMembershipResult{}
	}
	if policy.BotMembershipKnown && policy.Restricted {
		return modeMembershipResult{message: "Slack cannot auto-join apps to private channels; invite <@" + s.options.BotUserID + "> to activate this level."}
	}
	if s.members == nil {
		return modeMembershipResult{message: "The level was saved, but Tag could not join automatically; invite <@" + s.options.BotUserID + "> to activate it."}
	}
	membershipCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if !policy.BotMembershipKnown {
		channel, err := s.members.GetConversationInfoContext(membershipCtx, &slackapi.GetConversationInfoInput{ChannelID: request.ChannelID})
		if err != nil {
			logger.WithCtx(blackbox.Ctx{"error_type": fmt.Sprintf("%T", err), "slack_error_code": slackAPIErrorCode(err)}).Warn("Slack mode command could not inspect channel membership")
			return modeMembershipResult{message: "The level was saved, but Tag could not join automatically; invite <@" + s.options.BotUserID + "> if needed."}
		}
		if channel.IsMember {
			return modeMembershipResult{}
		}
		if channel.IsPrivate || channel.IsIM || channel.IsMpIM {
			return modeMembershipResult{message: "Slack cannot auto-join apps to this conversation; invite <@" + s.options.BotUserID + "> if Slack offers that option."}
		}
	}
	if _, _, _, err := s.members.JoinConversationContext(membershipCtx, request.ChannelID); err != nil {
		logger.WithCtx(blackbox.Ctx{"error_type": fmt.Sprintf("%T", err), "slack_error_code": slackAPIErrorCode(err)}).Warn("Slack mode command could not join channel")
		return modeMembershipResult{message: "The level was saved, but Tag could not join automatically; invite <@" + s.options.BotUserID + "> to activate it."}
	}
	return modeMembershipResult{changed: true, joined: true, message: "Tag also joined the channel."}
}

func (s *LiveIngress) leaveAfterModeOff(ctx context.Context, logger *blackbox.Logger, request ModeChangeRequest, policy ModeChangeResult) modeMembershipResult {
	if policy.BotMembershipKnown && !policy.BotIsMember {
		return modeMembershipResult{}
	}
	if s.members == nil {
		return modeMembershipResult{message: "The safe `observe` policy is active, but Tag could not leave automatically."}
	}
	membershipCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if !policy.BotMembershipKnown {
		channel, err := s.members.GetConversationInfoContext(membershipCtx, &slackapi.GetConversationInfoInput{ChannelID: request.ChannelID})
		if err != nil {
			logger.WithCtx(blackbox.Ctx{"error_type": fmt.Sprintf("%T", err), "slack_error_code": slackAPIErrorCode(err)}).Warn("Slack mode command could not inspect channel membership")
			return modeMembershipResult{message: "The safe `observe` policy is active, but Tag could not confirm whether it should leave."}
		}
		if !channel.IsMember {
			return modeMembershipResult{}
		}
		if channel.IsIM || channel.IsMpIM {
			return modeMembershipResult{message: "Slack does not support leaving this conversation, but the safe `observe` policy is active."}
		}
	}
	if _, err := s.members.LeaveConversationContext(membershipCtx, request.ChannelID); err != nil {
		logger.WithCtx(blackbox.Ctx{"error_type": fmt.Sprintf("%T", err), "slack_error_code": slackAPIErrorCode(err)}).Warn("Slack mode command could not leave channel")
		return modeMembershipResult{message: "The safe `observe` policy is active, but Slack would not let Tag leave automatically."}
	}
	return modeMembershipResult{changed: true, joined: false, message: "Tag also left the channel."}
}

func (s *LiveIngress) recordCommandMembership(ctx context.Context, logger *blackbox.Logger, request ModeChangeRequest, joined bool) {
	s.mu.Lock()
	handler := s.membershipHandler
	s.mu.Unlock()
	if handler == nil {
		return
	}
	change := BotMembershipChange{OrganizationID: request.OrganizationID, WorkspaceID: request.WorkspaceID, ChannelID: request.ChannelID, EventID: "slash-command/" + strings.TrimPrefix(request.Command, "/"), Joined: joined}
	if err := handler(ctx, change); err != nil {
		logger.WithCtx(blackbox.Ctx{"joined": joined, "error_type": fmt.Sprintf("%T", err)}).Warn("Slack mode command membership state refresh failed")
	}
}

type directiveCommand struct {
	Request   DirectiveConfigurationRequest
	TriggerID string
}

type directiveModalMetadata struct {
	WorkspaceID string `json:"workspace_id"`
	ChannelID   string `json:"channel_id"`
}

func normalizeSlashCommand(data any) (slackapi.SlashCommand, bool, error) {
	switch value := data.(type) {
	case slackapi.SlashCommand:
		return value, true, nil
	case *slackapi.SlashCommand:
		if value == nil {
			return slackapi.SlashCommand{}, false, errors.New("Slack slash command payload is nil")
		}
		return *value, true, nil
	default:
		return slackapi.SlashCommand{}, false, nil
	}
}

// NormalizeModeCommand validates the workspace-scoped participation commands.
// /tag-mode accepts a mode argument; the three fixed commands intentionally do
// not create a second participation model and map onto the same stored policy.
func NormalizeModeCommand(options LiveOptions, data any) (ModeChangeRequest, bool, error) {
	command, ok, err := normalizeSlashCommand(data)
	if err != nil || !ok {
		return ModeChangeRequest{}, false, err
	}
	mode := strings.ToLower(strings.TrimSpace(command.Text))
	switch command.Command {
	case modeSlashCommand:
	case proactiveSlashCommand:
		mode = "proactive"
	case assistSlashCommand:
		mode = "assist"
	case offSlashCommand:
		mode = "observe"
	default:
		return ModeChangeRequest{}, false, nil
	}
	if command.TeamID != options.TeamID || (command.APIAppID != "" && command.APIAppID != options.AppID) || command.ChannelID == "" || command.UserID == "" {
		return ModeChangeRequest{}, false, errors.New("Slack mode command scope is invalid")
	}
	return ModeChangeRequest{
		OrganizationID: options.OrganizationID,
		WorkspaceID:    command.TeamID,
		ChannelID:      command.ChannelID,
		UserID:         command.UserID,
		Command:        command.Command,
		Mode:           mode,
	}, true, nil
}

// NormalizeStatusCommand validates the read-only, channel-scoped /tag-status
// command. It returns the same scope shape used by a mode inspection, always
// with an empty Mode so the durable participation policy cannot be changed.
func NormalizeStatusCommand(options LiveOptions, data any) (ModeChangeRequest, bool, error) {
	command, ok, err := normalizeSlashCommand(data)
	if err != nil || !ok {
		return ModeChangeRequest{}, false, err
	}
	if command.Command != statusSlashCommand {
		return ModeChangeRequest{}, false, nil
	}
	if command.TeamID != options.TeamID || (command.APIAppID != "" && command.APIAppID != options.AppID) || command.ChannelID == "" || command.UserID == "" || strings.TrimSpace(command.Text) != "" {
		return ModeChangeRequest{}, false, errors.New("Slack status command scope is invalid")
	}
	return ModeChangeRequest{
		OrganizationID: options.OrganizationID,
		WorkspaceID:    command.TeamID,
		ChannelID:      command.ChannelID,
		UserID:         command.UserID,
		Command:        command.Command,
	}, true, nil
}

func NormalizeDirectiveCommand(options LiveOptions, data any) (directiveCommand, bool, error) {
	command, ok, err := normalizeSlashCommand(data)
	if err != nil || !ok {
		return directiveCommand{}, false, err
	}
	if command.Command != directiveSlashCommand {
		return directiveCommand{}, false, nil
	}
	if command.TeamID != options.TeamID || (command.APIAppID != "" && command.APIAppID != options.AppID) || command.ChannelID == "" || command.UserID == "" || command.TriggerID == "" {
		return directiveCommand{}, false, errors.New("Slack directive command scope is invalid")
	}
	return directiveCommand{Request: DirectiveConfigurationRequest{OrganizationID: options.OrganizationID, WorkspaceID: command.TeamID, ChannelID: command.ChannelID, UserID: command.UserID}, TriggerID: command.TriggerID}, true, nil
}

func NormalizeDirectiveSubmission(options LiveOptions, data any) (DirectiveConfigurationRequest, bool, error) {
	var callback slackapi.InteractionCallback
	switch value := data.(type) {
	case slackapi.InteractionCallback:
		callback = value
	case *slackapi.InteractionCallback:
		if value == nil {
			return DirectiveConfigurationRequest{}, false, errors.New("Slack interaction payload is nil")
		}
		callback = *value
	default:
		return DirectiveConfigurationRequest{}, false, nil
	}
	if callback.Type != slackapi.InteractionTypeViewSubmission || callback.View.CallbackID != directiveCallbackID {
		return DirectiveConfigurationRequest{}, false, nil
	}
	if callback.APIAppID != "" && callback.APIAppID != options.AppID {
		return DirectiveConfigurationRequest{}, false, errors.New("Slack directive submission app does not match installation")
	}
	if callback.Team.ID != options.TeamID || callback.User.ID == "" || callback.View.ID == "" || callback.View.State == nil {
		return DirectiveConfigurationRequest{}, false, errors.New("Slack directive submission scope is invalid")
	}
	var metadata directiveModalMetadata
	if err := json.Unmarshal([]byte(callback.View.PrivateMetadata), &metadata); err != nil || metadata.WorkspaceID != options.TeamID || metadata.ChannelID == "" {
		return DirectiveConfigurationRequest{}, false, errors.New("Slack directive modal metadata is invalid")
	}
	actions := callback.View.State.Values[directivePromptBlockID]
	prompt := strings.TrimSpace(actions[directivePromptActionID].Value)
	if prompt == "" || len([]rune(prompt)) > 3000 {
		return DirectiveConfigurationRequest{}, false, errors.New("Slack directive prompt is invalid")
	}
	return DirectiveConfigurationRequest{OrganizationID: options.OrganizationID, WorkspaceID: metadata.WorkspaceID, ChannelID: metadata.ChannelID, UserID: callback.User.ID, Prompt: prompt, InteractionID: callback.View.ID}, true, nil
}

func directiveModal(request DirectiveConfigurationRequest, current DirectiveConfiguration) slackapi.ModalViewRequest {
	metadata, _ := json.Marshal(directiveModalMetadata{WorkspaceID: request.WorkspaceID, ChannelID: request.ChannelID})
	plain := slackapi.NewPlainTextInputBlockElement(slackapi.NewTextBlockObject("plain_text", "Describe how tag should behave in this channel", false, false), directivePromptActionID)
	plain.Multiline = true
	plain.MinLength = 1
	plain.MaxLength = 3000
	plain.FocusOnLoad = true
	if current.Prompt != "" {
		plain.InitialValue = current.Prompt
	}
	revision := "No active directive."
	if current.Revision > 0 {
		revision = fmt.Sprintf("Editing active revision %d.", current.Revision)
	}
	alert := slackapi.NewAlertBlock(
		slackapi.NewTextBlockObject("plain_text", "This directive affects only this channel. It cannot grant tools or override safety policy.", false, false),
		slackapi.AlertBlockOptionLevel(slackapi.AlertLevelInfo),
		slackapi.AlertBlockOptionBlockID("tos_tag_directive_scope"),
	)
	intro := slackapi.NewSectionBlock(slackapi.NewTextBlockObject("mrkdwn", "Set the operating directive for this channel. It guides both the ambient classifier and admitted full-agent work.\n\n"+revision, false, false), nil, nil)
	input := slackapi.NewInputBlock(directivePromptBlockID, slackapi.NewTextBlockObject("plain_text", "Channel directive", false, false), slackapi.NewTextBlockObject("plain_text", "Example: Investigate each alert, correlate it with OpenTelemetry and related systems, and report the most likely root cause with evidence.", false, false), plain)
	return slackapi.ModalViewRequest{
		Type:            slackapi.VTModal,
		Title:           slackapi.NewTextBlockObject("plain_text", "Channel directive", false, false),
		Submit:          slackapi.NewTextBlockObject("plain_text", "Save directive", false, false),
		Close:           slackapi.NewTextBlockObject("plain_text", "Cancel", false, false),
		Blocks:          slackapi.Blocks{BlockSet: []slackapi.Block{alert, intro, input}},
		PrivateMetadata: string(metadata),
		CallbackID:      directiveCallbackID,
	}
}

func ephemeralCommandResponse(message string) map[string]any {
	return map[string]any{"response_type": "ephemeral", "text": message}
}

func ephemeralStatusResponse(policy ModeChangeResult, directive DirectiveConfiguration, directiveAvailable bool) map[string]any {
	table := slackapi.NewTableBlock("tos_tag_channel_status").WithColumnSettings(
		slackapi.ColumnSetting{Align: slackapi.ColumnAlignmentLeft, IsWrapped: true},
		slackapi.ColumnSetting{Align: slackapi.ColumnAlignmentLeft, IsWrapped: true},
	)
	addStatusRow := func(label, value string) {
		table.AddRow(slackapi.NewTableRawTextCell(label), slackapi.NewTableRawTextCell(value))
	}
	addStatusRow("Participation", statusModeDescription(policy.Mode))
	addStatusRow("Directive", statusDirectiveDescription(directive, directiveAvailable))
	addStatusRow("Availability", statusAvailabilityDescription(policy))
	addStatusRow("Tag presence", statusPresenceDescription(policy))
	addStatusRow("Channel scope", statusScopeDescription(policy))

	header := slackapi.NewSectionBlock(
		slackapi.NewTextBlockObject("mrkdwn", "*Tag channel status*", false, false),
		nil,
		nil,
	)
	footer := slackapi.NewContextBlock(
		"tos_tag_channel_status_controls",
		slackapi.NewTextBlockObject("mrkdwn", "Controls: `/tag-assist` · `/tag-proactive` · `/tag-off` · `/tag-directive`", false, false),
	)
	return map[string]any{
		"response_type": "ephemeral",
		"text":          "Tag channel status: " + policy.Mode,
		"blocks":        []slackapi.Block{header, table, footer},
	}
}

func statusModeDescription(mode string) string {
	switch mode {
	case "observe":
		return "off (observe) — retains authorized context but does not react or answer"
	case "mention":
		return "mention — responds to direct mentions and active Tag threads"
	case "assist":
		return "assist — answers useful ambient questions and authorized interventions"
	case "proactive":
		return "proactive — may start classifier-approved background work"
	case "":
		return "Unknown"
	default:
		return mode
	}
}

func statusDirectiveDescription(directive DirectiveConfiguration, available bool) string {
	if !available {
		return "Unavailable"
	}
	if strings.TrimSpace(directive.Prompt) == "" || directive.Revision <= 0 {
		return "No channel directive is configured"
	}
	prompt := strings.Join(strings.Fields(directive.Prompt), " ")
	const maximumRunes = 1200
	runes := []rune(prompt)
	if len(runes) > maximumRunes {
		prompt = string(runes[:maximumRunes-1]) + "…"
	}
	return fmt.Sprintf("Revision %d — %s", directive.Revision, prompt)
}

func statusAvailabilityDescription(policy ModeChangeResult) string {
	issues := make([]string, 0, 3)
	if !policy.WorkspaceEnabled {
		issues = append(issues, "workspace disabled")
	}
	if !policy.Enrolled {
		issues = append(issues, "channel not enrolled")
	}
	if policy.KillSwitched {
		issues = append(issues, "channel kill switch active")
	}
	if len(issues) == 0 {
		return "Enabled"
	}
	return "Unavailable — " + strings.Join(issues, "; ")
}

func statusPresenceDescription(policy ModeChangeResult) string {
	if !policy.BotMembershipKnown {
		return "Unknown — Slack membership has not been reconciled"
	}
	if policy.BotIsMember {
		return "Joined"
	}
	if policy.Restricted {
		return "Not joined — invite Tag to this private channel if active participation is wanted"
	}
	return "Not joined"
}

func statusScopeDescription(policy ModeChangeResult) string {
	if policy.Restricted {
		return "Private or restricted — context and output stay channel-local"
	}
	return "Public channel"
}

func NormalizeApprovalInteraction(options LiveOptions, data any) (ApprovalInteraction, bool, error) {
	var callback slackapi.InteractionCallback
	switch value := data.(type) {
	case slackapi.InteractionCallback:
		callback = value
	case *slackapi.InteractionCallback:
		if value == nil {
			return ApprovalInteraction{}, false, errors.New("Slack interaction payload is nil")
		}
		callback = *value
	default:
		return ApprovalInteraction{}, false, nil
	}
	if callback.Type != slackapi.InteractionTypeBlockActions {
		return ApprovalInteraction{}, false, nil
	}
	if callback.APIAppID != "" && callback.APIAppID != options.AppID {
		return ApprovalInteraction{}, false, errors.New("Slack interaction app does not match installation")
	}
	if callback.Team.ID != options.TeamID || callback.User.ID == "" || len(callback.ActionCallback.BlockActions) != 1 {
		return ApprovalInteraction{}, false, errors.New("Slack interaction scope is invalid")
	}
	action := callback.ActionCallback.BlockActions[0]
	if action == nil || action.Value == "" {
		return ApprovalInteraction{}, false, errors.New("Slack interaction action is invalid")
	}
	approve := false
	switch action.ActionID {
	case "tos_tag_approval_approve":
		approve = true
	case "tos_tag_approval_deny":
	default:
		return ApprovalInteraction{}, false, nil
	}
	channelID := callback.Channel.ID
	if channelID == "" {
		channelID = callback.Container.ChannelID
	}
	if channelID == "" {
		return ApprovalInteraction{}, false, errors.New("Slack interaction channel is missing")
	}
	messageTS := callback.Container.MessageTs
	if messageTS == "" {
		messageTS = callback.Message.Timestamp
	}
	if messageTS == "" {
		messageTS = callback.MessageTs
	}
	if messageTS == "" {
		return ApprovalInteraction{}, false, errors.New("Slack interaction message is missing")
	}
	return ApprovalInteraction{OrganizationID: options.OrganizationID, WorkspaceID: callback.Team.ID, ChannelID: channelID, UserID: callback.User.ID, ApprovalID: action.Value, MessageTS: messageTS, Approve: approve}, true, nil
}

func slackEnvelopeLogContext(envelope types.SlackEnvelope) blackbox.Ctx {
	return blackbox.Ctx{
		"organization_id": envelope.OrganizationID,
		"event_id":        envelope.EventID,
		"channel_id":      envelope.ChannelID,
		"message_ts":      envelope.MessageTS,
		"thread_ts":       envelope.ThreadTS,
		"event_kind":      string(envelope.Kind),
		"event_subtype":   envelope.Subtype,
		"is_mention":      envelope.IsMention,
		"is_bot_event":    envelope.BotID != "",
		"text_bytes":      len(envelope.Text),
	}
}

func (s *LiveIngress) Stop(ctx context.Context) error {
	s.mu.Lock()
	if !s.started && !s.stopping {
		s.mu.Unlock()
		return nil
	}
	cancel, done := s.cancel, s.done
	s.started = false
	s.stopping = true
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	recoveryDone := make(chan struct{})
	go func() {
		s.recoveryWG.Wait()
		close(recoveryDone)
	}()
	select {
	case <-recoveryDone:
		s.mu.Lock()
		s.stopping = false
		s.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func NormalizeEventsAPI(organizationID, botUserID, envelopeID string, data any) (types.SlackEnvelope, bool, error) {
	event, ok := data.(slackevents.EventsAPIEvent)
	if !ok || event.Type != slackevents.CallbackEvent {
		return types.SlackEnvelope{}, false, nil
	}
	var callback *slackevents.EventsAPICallbackEvent
	switch value := event.Data.(type) {
	case *slackevents.EventsAPICallbackEvent:
		callback = value
	case slackevents.EventsAPICallbackEvent:
		callback = &value
	}
	if callback == nil {
		return types.SlackEnvelope{}, false, errors.New("Slack callback payload is malformed")
	}
	base := types.SlackEnvelope{
		OrganizationID: organizationID,
		EnvelopeID:     envelopeID,
		EventID:        callback.EventID,
		TeamID:         callback.TeamID,
		EventTime:      time.Unix(int64(callback.EventTime), 0).UTC(),
		ReceivedAt:     time.Now().UTC(),
	}
	switch inner := event.InnerEvent.Data.(type) {
	case *slackevents.AppMentionEvent:
		base.ChannelID, base.MessageTS, base.ThreadTS = inner.Channel, inner.TimeStamp, inner.ThreadTimeStamp
		base.UserID, base.BotID, base.Text = inner.User, inner.BotID, inner.Text
		base.Kind, base.IsMention, base.OriginTag = types.SlackEventMessage, true, "slack_app_mention"
		base.EventID = canonicalMessageEventID(base.EventID, base.TeamID, base.ChannelID, base.MessageTS)
		return base, true, nil
	case *slackevents.MessageEvent:
		base.ChannelID, base.MessageTS, base.ThreadTS = inner.Channel, inner.TimeStamp, inner.ThreadTimeStamp
		base.UserID, base.BotID, base.Text, base.Subtype = inner.User, inner.BotID, inner.Text, inner.SubType
		base.ChannelKind = inner.ChannelType
		base.Restricted = inner.ChannelType == slackevents.ChannelTypeGroup || inner.ChannelType == slackevents.ChannelTypeIM || inner.ChannelType == slackevents.ChannelTypeMPIM
		base.Kind, base.OriginTag = types.SlackEventMessage, "slack_message"
		switch inner.SubType {
		case "message_changed":
			base.Kind = types.SlackEventEdit
			if inner.Message != nil {
				base.TargetTS, base.MessageTS, base.ThreadTS = inner.Message.Timestamp, inner.Message.Timestamp, inner.Message.ThreadTimestamp
				base.UserID, base.BotID, base.Text = inner.Message.User, inner.Message.BotID, inner.Message.Text
			}
		case "message_deleted":
			base.Kind, base.TargetTS, base.MessageTS, base.Text = types.SlackEventDelete, inner.DeletedTimeStamp, inner.DeletedTimeStamp, ""
		}
		base.IsMention = botUserID != "" && strings.Contains(base.Text, "<@"+botUserID+">")
		if base.Kind == types.SlackEventMessage {
			base.EventID = canonicalMessageEventID(base.EventID, base.TeamID, base.ChannelID, base.MessageTS)
		}
		if base.EventTime.IsZero() {
			base.EventTime = slackTimestamp(base.MessageTS)
		}
		return base, true, nil
	default:
		return types.SlackEnvelope{}, false, nil
	}
}

// canonicalMessageEventID coalesces Slack's app_mention and message callbacks
// for the same message while preserving Slack's callback ID as a fallback.
// Mutation callbacks keep their own callback IDs so every edit/delete remains
// independently observable.
func canonicalMessageEventID(fallback, teamID, channelID, messageTS string) string {
	if teamID == "" || channelID == "" || messageTS == "" {
		return fallback
	}
	return "message/" + teamID + "/" + channelID + "/" + messageTS
}

func (d *LiveDelivery) Send(ctx context.Context, request types.SlackDeliveryRequest) (types.SlackDeliveryResult, error) {
	started := time.Now()
	logger := d.logger
	if logger == nil {
		logger = blackbox.New()
	}
	requestLogger := logger.WithCtx(blackbox.Ctx{
		"delivery_id": request.ID, "channel_id": request.Destination.ChannelID,
		"thread_ts": request.Destination.ThreadTS, "segment_count": len(request.Result.Segments),
	})
	requestLogger.Info("Slack delivery requested")
	if request.Destination.TeamID != d.teamID {
		requestLogger.Warn("Slack delivery denied for mismatched workspace")
		return types.SlackDeliveryResult{}, errors.New("Slack destination team does not match the configured installation")
	}
	payloads, err := d.renderer.Render(request.Result)
	if err != nil {
		requestLogger.WithCtx(blackbox.Ctx{"error_type": fmt.Sprintf("%T", err)}).Error("Slack delivery rendering failed")
		return types.SlackDeliveryResult{}, err
	}
	if request.Destination.UpdateTS != "" && len(payloads) != 1 {
		return types.SlackDeliveryResult{}, errors.New("Slack message updates must render as exactly one payload")
	}
	existing, err := d.deliveryParts(ctx, request)
	if err != nil {
		requestLogger.WithCtx(blackbox.Ctx{"error_type": fmt.Sprintf("%T", err)}).Error("Slack delivery reconciliation failed")
		return types.SlackDeliveryResult{}, fmt.Errorf("reconcile Slack delivery: %w", err)
	}
	requestLogger.WithCtx(blackbox.Ctx{"payload_count": len(payloads), "reconciled_part_count": len(existing)}).Info("Slack delivery reconciliation completed")
	var firstTimestamp string
	for _, timestamp := range existing {
		if firstTimestamp == "" || timestamp < firstTimestamp {
			firstTimestamp = timestamp
		}
	}
	for index, payload := range payloads {
		if _, alreadyDelivered := existing[index+1]; alreadyDelivered {
			continue
		}
		regularRendered, err := downgradeAgentUIBlocks(payload.Blocks)
		if err != nil {
			return types.SlackDeliveryResult{}, err
		}
		regularBlocks, err := slackBlocksFromRendered(regularRendered)
		if err != nil {
			return types.SlackDeliveryResult{}, err
		}
		options := []slackapi.MsgOption{
			slackapi.MsgOptionText(payload.Text, false),
			slackapi.MsgOptionBlocks(regularBlocks...),
			slackapi.MsgOptionDisableLinkUnfurl(),
			slackapi.MsgOptionDisableMediaUnfurl(),
			slackapi.MsgOptionMetadata(slackapi.SlackMetadata{EventType: "tos_tag_delivery", EventPayload: map[string]any{"delivery_id": string(request.ID), "part": index + 1}}),
		}
		if request.Destination.ThreadTS != "" && request.Destination.UpdateTS == "" && !(request.Destination.StreamTS != "" && index == 0) {
			options = append(options, slackapi.MsgOptionTS(request.Destination.ThreadTS))
		}
		var timestamp string
		if request.Destination.StreamTS != "" && index == 0 {
			// A Thinking Steps stream is opened in chunks mode, so its final
			// content must remain in chunks mode as well. Sending markdown_text or
			// top-level blocks here makes Slack reject the close with
			// streaming_mode_mismatch. A blocks chunk preserves the renderer's
			// validated Block Kit result and finalizes it atomically with metadata.
			nativeBlocks, conversionErr := slackBlocksFromRendered(payload.Blocks)
			if conversionErr != nil {
				return types.SlackDeliveryResult{}, conversionErr
			}
			var finalChunk slackapi.StreamChunk = slackapi.NewMarkdownTextChunk(payload.Text)
			if len(nativeBlocks) > 0 {
				finalChunk = slackapi.NewBlocksChunk(nativeBlocks...)
			}
			streamOptions := []slackapi.MsgOption{
				slackapi.MsgOptionChunks(finalChunk),
				slackapi.MsgOptionDisableLinkUnfurl(),
				slackapi.MsgOptionDisableMediaUnfurl(),
				slackapi.MsgOptionMetadata(slackapi.SlackMetadata{EventType: "tos_tag_delivery", EventPayload: map[string]any{"delivery_id": string(request.ID), "part": index + 1}}),
			}
			_, timestamp, err = d.api.StopStreamContext(ctx, request.Destination.ChannelID, request.Destination.StreamTS, streamOptions...)
			if timestamp == "" {
				timestamp = request.Destination.StreamTS
			}
			if err != nil {
				failureContext := blackbox.Ctx{"part": index + 1, "error_type": fmt.Sprintf("%T", err)}
				if code := slackAPIErrorCode(err); code != "" {
					failureContext["slack_error_code"] = code
				}
				requestLogger.WithCtx(failureContext).Warn("Slack stream finalization failed; falling back to normal delivery")
				expiredStreamText := "Agent work completed. The final answer follows in this thread."
				expiredStreamBlock := slackapi.NewSectionBlock(slackapi.NewTextBlockObject(slackapi.MarkdownType, expiredStreamText, false, false), nil, nil)
				if _, _, _, updateErr := d.api.UpdateMessageContext(
					ctx,
					request.Destination.ChannelID,
					request.Destination.StreamTS,
					slackapi.MsgOptionText(expiredStreamText, false),
					slackapi.MsgOptionBlocks(expiredStreamBlock),
					slackapi.MsgOptionDisableLinkUnfurl(),
					slackapi.MsgOptionDisableMediaUnfurl(),
				); updateErr != nil {
					requestLogger.WithCtx(blackbox.Ctx{"part": index + 1, "error_type": fmt.Sprintf("%T", updateErr)}).Warn("Slack expired stream cleanup failed; continuing with final fallback delivery")
				}
				fallbackOptions := append([]slackapi.MsgOption(nil), options...)
				if request.Destination.ThreadTS != "" {
					fallbackOptions = append(fallbackOptions, slackapi.MsgOptionTS(request.Destination.ThreadTS))
				}
				_, timestamp, err = d.api.PostMessageContext(ctx, request.Destination.ChannelID, fallbackOptions...)
			}
		} else if request.Destination.UpdateTS != "" {
			_, timestamp, _, err = d.api.UpdateMessageContext(ctx, request.Destination.ChannelID, request.Destination.UpdateTS, options...)
			if timestamp == "" {
				timestamp = request.Destination.UpdateTS
			}
		} else {
			_, timestamp, err = d.api.PostMessageContext(ctx, request.Destination.ChannelID, options...)
		}
		if err != nil {
			failureContext := blackbox.Ctx{"part": index + 1, "error_type": fmt.Sprintf("%T", err)}
			if code := slackAPIErrorCode(err); code != "" {
				failureContext["slack_error_code"] = code
			}
			requestLogger.WithCtx(failureContext).Error("Slack delivery part failed")
			return types.SlackDeliveryResult{}, err
		}
		requestLogger.WithCtx(blackbox.Ctx{"part": index + 1, "message_ts": timestamp}).Info("Slack delivery part accepted")
		if firstTimestamp == "" {
			firstTimestamp = timestamp
		}
	}
	result := types.SlackDeliveryResult{MessageTS: firstTimestamp, DeliveredAt: time.Now().UTC(), Duplicate: len(existing) == len(payloads)}
	requestLogger.WithCtx(blackbox.Ctx{"duration_ms": time.Since(started).Milliseconds(), "duplicate": result.Duplicate, "message_ts": result.MessageTS}).Info("Slack delivery completed")
	return result, nil
}

// progressMessage reconciles a started Thinking Steps stream by immutable
// metadata. This closes the crash window between Slack accepting startStream
// and the job persisting the returned timestamp.
func (d *LiveDelivery) progressMessage(ctx context.Context, channelID, threadTS, idempotencyKey string) (string, error) {
	cursor := ""
	for page := 0; page < 20; page++ {
		var messages []slackapi.Message
		var hasMore bool
		var next string
		if threadTS != "" {
			var err error
			messages, hasMore, next, err = d.api.GetConversationRepliesContext(ctx, &slackapi.GetConversationRepliesParameters{ChannelID: channelID, Timestamp: threadTS, Cursor: cursor, Limit: 100, IncludeAllMetadata: true})
			if err != nil {
				return "", err
			}
		} else {
			response, err := d.api.GetConversationHistoryContext(ctx, &slackapi.GetConversationHistoryParameters{ChannelID: channelID, Cursor: cursor, Limit: 100, IncludeAllMetadata: true})
			if err != nil {
				return "", err
			}
			messages, hasMore, next = response.Messages, response.HasMore, response.ResponseMetaData.NextCursor
		}
		for _, message := range messages {
			if message.Metadata.EventType == "tos_tag_progress" && fmt.Sprint(message.Metadata.EventPayload["idempotency_key"]) == idempotencyKey {
				return message.Timestamp, nil
			}
		}
		if !hasMore || next == "" {
			break
		}
		cursor = next
	}
	return "", nil
}

func (d *LiveDelivery) React(ctx context.Context, request types.SlackReactionRequest) (types.SlackReactionResult, error) {
	started := time.Now()
	logger := d.logger
	if logger == nil {
		logger = blackbox.New()
	}
	requestLogger := logger.WithCtx(blackbox.Ctx{
		"reaction_idempotency_key": request.IdempotencyKey,
		"channel_id":               request.ChannelID,
		"message_ts":               request.MessageTS,
		"emoji":                    request.Emoji,
	})
	requestLogger.Info("Slack reaction requested")
	if request.TeamID != d.teamID {
		requestLogger.Warn("Slack reaction denied for mismatched workspace")
		return types.SlackReactionResult{}, errors.New("Slack reaction team does not match the configured installation")
	}
	if request.ChannelID == "" || request.MessageTS == "" || !validReactionName(request.Emoji) {
		return types.SlackReactionResult{}, errors.New("invalid Slack reaction request")
	}
	err := d.api.AddReactionContext(ctx, request.Emoji, slackapi.ItemRef{Channel: request.ChannelID, Timestamp: request.MessageTS})
	duplicate := false
	if err != nil {
		var slackError slackapi.SlackErrorResponse
		if errors.As(err, &slackError) && slackError.Err == "already_reacted" {
			duplicate = true
		} else {
			failureContext := blackbox.Ctx{"duration_ms": time.Since(started).Milliseconds(), "error_type": fmt.Sprintf("%T", err)}
			if code := slackAPIErrorCode(err); code != "" {
				failureContext["slack_error_code"] = code
			}
			requestLogger.WithCtx(failureContext).Error("Slack reaction failed")
			return types.SlackReactionResult{}, err
		}
	}
	result := types.SlackReactionResult{AppliedAt: time.Now().UTC(), Duplicate: duplicate}
	requestLogger.WithCtx(blackbox.Ctx{"duration_ms": time.Since(started).Milliseconds(), "duplicate": duplicate}).Info("Slack reaction completed")
	return result, nil
}

func validReactionName(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '_' && character != '-' && character != '+' {
			return false
		}
	}
	return true
}

// downgradeAgentUIBlocks preserves rich agent blocks for the streaming path
// while making ordinary post/update and stream-fallback delivery reliable.
// Slack rejects Card, Carousel, and Data Table blocks on chat.postMessage even
// though they are valid inside a streaming blocks chunk.
func downgradeAgentUIBlocks(rendered []map[string]any) ([]map[string]any, error) {
	blocks := make([]map[string]any, 0, len(rendered))
	for _, block := range rendered {
		switch block["type"] {
		case "card":
			text := agentCardText(block)
			if text == "" {
				return nil, errors.New("rendered Slack card lacks fallback content")
			}
			blocks = append(blocks, map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": text}, "expand": true})
		case "carousel":
			elements, ok := block["elements"].([]any)
			if !ok || len(elements) == 0 {
				return nil, errors.New("rendered Slack carousel lacks cards")
			}
			parts := make([]string, 0, len(elements))
			for _, element := range elements {
				card, ok := element.(map[string]any)
				if !ok {
					return nil, errors.New("rendered Slack carousel has an invalid card")
				}
				parts = append(parts, agentCardText(card))
			}
			text := strings.Join(parts, "\n\n")
			if text == "" || len([]rune(text)) > 3000 {
				return nil, errors.New("rendered Slack carousel fallback exceeds one section")
			}
			blocks = append(blocks, map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": text}, "expand": true})
		case "data_table":
			rows, ok := block["rows"].([]any)
			if !ok || len(rows) < 2 {
				return nil, errors.New("rendered Slack data table lacks rows")
			}
			first, ok := rows[0].([]any)
			if !ok || len(first) == 0 {
				return nil, errors.New("rendered Slack data table lacks columns")
			}
			columns := make([]any, len(first))
			for index := range columns {
				columns[index] = map[string]any{"is_wrapped": false}
			}
			blocks = append(blocks, map[string]any{"type": "table", "column_settings": columns, "rows": rows})
		default:
			blocks = append(blocks, block)
		}
	}
	return blocks, nil
}

func agentCardText(card map[string]any) string {
	value := func(field string) string {
		object, _ := card[field].(map[string]any)
		text, _ := object["text"].(string)
		return strings.TrimSpace(text)
	}
	parts := make([]string, 0, 4)
	if title := value("title"); title != "" {
		parts = append(parts, "*"+title+"*")
	}
	for _, field := range []string{"subtitle", "body", "subtext"} {
		if text := value(field); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

// slackBlocksFromRendered preserves the renderer's validated JSON exactly.
// Decoding through slackapi.Blocks and then encoding again can introduce zero
// values such as an empty table-column alignment, which Slack rejects.
func slackBlocksFromRendered(rendered []map[string]any) ([]slackapi.Block, error) {
	blocks := make([]slackapi.Block, 0, len(rendered))
	for index, value := range rendered {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("encode rendered Slack block %d: %w", index, err)
		}
		block, err := slackapi.BlockFromJSON(string(encoded))
		if err != nil {
			return nil, fmt.Errorf("convert rendered Slack block %d: %w", index, err)
		}
		blocks = append(blocks, block)
	}
	return blocks, nil
}

// deliveryParts finds previously accepted parts by immutable Slack metadata.
// Send calls this before every post, so a worker crash after Slack acceptance
// but before Mongo completion resumes at the first missing part.
func (d *LiveDelivery) deliveryParts(ctx context.Context, request types.SlackDeliveryRequest) (map[int]string, error) {
	found := make(map[int]string)
	cursor := ""
	for page := 0; page < 20; page++ {
		var messages []slackapi.Message
		var hasMore bool
		var next string
		if request.Destination.ThreadTS != "" {
			var err error
			messages, hasMore, next, err = d.api.GetConversationRepliesContext(ctx, &slackapi.GetConversationRepliesParameters{ChannelID: request.Destination.ChannelID, Timestamp: request.Destination.ThreadTS, Cursor: cursor, Limit: 100, IncludeAllMetadata: true})
			if err != nil {
				return nil, err
			}
		} else {
			response, err := d.api.GetConversationHistoryContext(ctx, &slackapi.GetConversationHistoryParameters{ChannelID: request.Destination.ChannelID, Cursor: cursor, Limit: 100, IncludeAllMetadata: true})
			if err != nil {
				return nil, err
			}
			messages, hasMore, next = response.Messages, response.HasMore, response.ResponseMetaData.NextCursor
		}
		for _, message := range messages {
			if message.Metadata.EventType != "tos_tag_delivery" || fmt.Sprint(message.Metadata.EventPayload["delivery_id"]) != string(request.ID) {
				continue
			}
			part, partErr := strconv.Atoi(fmt.Sprint(message.Metadata.EventPayload["part"]))
			if partErr == nil && part > 0 {
				found[part] = message.Timestamp
			}
		}
		if !hasMore || next == "" {
			break
		}
		cursor = next
	}
	return found, nil
}

func slackTimestamp(value string) time.Time {
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return time.Time{}
	}
	whole := int64(seconds)
	return time.Unix(whole, int64((seconds-float64(whole))*float64(time.Second))).UTC()
}
