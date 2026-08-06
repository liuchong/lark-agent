// Package realtime adapts bot-visible Lark events into the durable agent queue.
package realtime

import (
	"bufio"
	"context"
	"io"
	"strings"
	"time"

	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/agent/ingest"
	"github.com/liuchong/lark-agent/internal/apperr"
	servicelark "github.com/liuchong/lark-agent/internal/lark"
)

const maxEventBytes = 1024 * 1024

var sourceNow = func() time.Time {
	return time.Now().UTC()
}

// Consumer streams one processed JSON event per line until the context ends.
type Consumer interface {
	Consume(context.Context, io.Writer) error
}

type resourceAwareConsumer interface {
	ConsumeWithResources(
		context.Context,
		io.Writer,
		func(context.Context, servicelark.ResourceSignal) error,
	) error
}

// Runner is one restartable real-time intake session.
type Runner interface {
	Run(context.Context) error
}

// Store persists pre-classified work before a scheduler can claim it.
type Store interface {
	RecordWorkIntake(context.Context, domain.WorkItem) (domain.IntakeReceipt, error)
}

// Config defines the bot-visible real-time intake boundary.
type Config struct {
	OwnerOpenID          string
	AssistantOpenIDs     []string
	AssistantNames       []string
	AssistantReplyScope  domain.ReplyScope
	ConfiguredChatIDs    []string
	Classify             func(context.Context, domain.WorkItem) (domain.Decision, error)
	HandleResourceSignal func(context.Context, servicelark.ResourceSignal) error
}

// Source converts bot events to the same durable work shape as polling.
type Source struct {
	consumer Consumer
	store    Store
	cfg      Config
}

// NewSource constructs one real-time source.
func NewSource(consumer Consumer, store Store, cfg Config) *Source {
	if cfg.AssistantReplyScope == "" {
		cfg.AssistantReplyScope = domain.ReplyScopeAllGroups
	}
	return &Source{consumer: consumer, store: store, cfg: cfg}
}

// Supervise restarts failed event sessions while the durable poll fallback and
// scheduler continue independently.
func Supervise(
	ctx context.Context,
	runner Runner,
	initialBackoff time.Duration,
	maxBackoff time.Duration,
	report func(error),
) {
	if initialBackoff <= 0 {
		initialBackoff = time.Second
	}
	if maxBackoff < initialBackoff {
		maxBackoff = 30 * time.Second
	}
	backoff := initialBackoff
	for {
		err := runner.Run(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil && report != nil {
			report(err)
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-timer.C:
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// Run consumes until the event stream ends or fails.
func (s *Source) Run(ctx context.Context) error {
	if s.consumer == nil {
		return errs.NewInternalError(errs.SubtypeFailedPrecondition, "real-time event consumer is not configured")
	}
	if s.store == nil {
		return errs.NewInternalError(errs.SubtypeFailedPrecondition, "real-time event store is not configured")
	}

	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()
	reader, writer := io.Pipe()
	consumeErr := make(chan error, 1)
	go func() {
		var err error
		if consumer, ok := s.consumer.(resourceAwareConsumer); ok && s.cfg.HandleResourceSignal != nil {
			err = consumer.ConsumeWithResources(streamCtx, writer, s.cfg.HandleResourceSignal)
		} else {
			err = s.consumer.Consume(streamCtx, writer)
		}
		_ = writer.CloseWithError(err)
		consumeErr <- err
	}()

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), maxEventBytes)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			cancelStream()
			_ = reader.CloseWithError(err)
			<-consumeErr
			return err
		}
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		event, err := ingest.NormalizeRealtime(line)
		if err != nil {
			cancelStream()
			_ = reader.CloseWithError(err)
			<-consumeErr
			return err
		}
		event.InAssistantScope = event.InAssistantScope || contains(s.cfg.ConfiguredChatIDs, event.ChatID)
		if !s.accept(event) {
			continue
		}
		if event.CreatedAt.IsZero() {
			event.CreatedAt = sourceNow()
		}
		if event.ChatType == "p2p" {
			event.ChatPartnerID = firstAssistantOpenID(s.cfg.AssistantOpenIDs)
			if event.ChatPartnerID == "" {
				event.ChatName = firstNonEmpty(s.cfg.AssistantNames)
			}
		}
		item := domain.NewWorkItem(event)
		if s.cfg.Classify != nil {
			decision, err := s.cfg.Classify(ctx, item)
			if err != nil {
				cancelStream()
				_ = reader.CloseWithError(err)
				<-consumeErr
				return err
			}
			item.WorkKind = decision.WorkKind
			item.Priority = decision.Priority
		}
		if _, err := s.store.RecordWorkIntake(ctx, item); err != nil {
			cancelStream()
			_ = reader.CloseWithError(err)
			<-consumeErr
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		if consumerErr := <-consumeErr; consumerErr != nil {
			return consumerErr
		}
		return errs.NewInternalError(errs.SubtypeInvalidResponse, "read Lark real-time event stream").WithCause(err)
	}
	return <-consumeErr
}

func (s *Source) accept(event domain.NormalizedEvent) bool {
	if event.MessageID == "" ||
		(event.SenderType != "" && event.SenderType != "user") {
		return false
	}
	if event.ChatType == "p2p" {
		return event.SenderID == s.cfg.OwnerOpenID &&
			(firstAssistantOpenID(s.cfg.AssistantOpenIDs) != "" ||
				firstNonEmpty(s.cfg.AssistantNames) != "")
	}
	if event.SenderID != s.cfg.OwnerOpenID {
		return false
	}
	if s.cfg.AssistantReplyScope == domain.ReplyScopeConfiguredGroups && !event.InAssistantScope {
		return false
	}
	for _, assistantOpenID := range s.cfg.AssistantOpenIDs {
		if strings.TrimSpace(assistantOpenID) == "" {
			continue
		}
		if event.MentionsUser(assistantOpenID) {
			return true
		}
	}
	for _, mention := range event.Mentions {
		for _, assistantName := range s.cfg.AssistantNames {
			assistantName = strings.TrimSpace(assistantName)
			if assistantName != "" &&
				strings.EqualFold(strings.TrimSpace(mention.Name), assistantName) {
				return true
			}
		}
	}
	return false
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func firstAssistantOpenID(openIDs []string) string {
	for _, openID := range openIDs {
		if strings.TrimSpace(openID) != "" {
			return openID
		}
	}
	return ""
}

func firstNonEmpty(values []string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
