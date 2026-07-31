package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

const maxUsageParserState = 64 << 10

type usageObservedBody struct {
	body        io.ReadCloser
	contentType string
	state       []byte
	overflow    bool
	reachedEOF  bool
	once        sync.Once
	complete    func(usageMeasurement)
}

type usageMeasurement struct {
	InputTokens        *int64
	OutputTokens       *int64
	CacheReadTokens    *int64
	CacheWriteTokens   *int64
	ReportedCostMicros *int64
	ReportedCurrency   string
	CaptureStatus      domain.UsageCaptureStatus
	ErrorCategory      string
}

type usageSubject struct {
	Kind           domain.UsageSubjectKind
	ID             string
	ProjectID      string
	ServiceID      string
	UserID         string
	GrantScope     string
	GrantScopeID   string
	GrantScopeName string
}

func observeUsageResponse(resp *http.Response, complete func(usageMeasurement)) {
	if resp == nil || resp.Body == nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return
	}
	resp.Body = &usageObservedBody{
		body: resp.Body, contentType: resp.Header.Get("Content-Type"), complete: complete,
	}
}

func (b *usageObservedBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	if n > 0 {
		b.appendState(p[:n])
	}
	if errors.Is(err, io.EOF) {
		b.reachedEOF = true
		b.finish()
	}
	return n, err
}

func (b *usageObservedBody) Close() error {
	// A caller closing before EOF did not receive a completed provider response;
	// do not fabricate an unavailable event from a truncated stream.
	if b.reachedEOF {
		b.finish()
	}
	return b.body.Close()
}

func (b *usageObservedBody) appendState(chunk []byte) {
	if len(chunk) >= maxUsageParserState {
		b.state = append(b.state[:0], chunk[len(chunk)-maxUsageParserState:]...)
		b.overflow = true
		return
	}
	if len(b.state)+len(chunk) > maxUsageParserState {
		drop := len(b.state) + len(chunk) - maxUsageParserState
		copy(b.state, b.state[drop:])
		b.state = b.state[:len(b.state)-drop]
		b.overflow = true
	}
	b.state = append(b.state, chunk...)
}

func (b *usageObservedBody) finish() {
	b.once.Do(func() {
		b.complete(parseUsageMeasurement(b.contentType, b.state, b.overflow))
	})
}

func parseUsageMeasurement(contentType string, state []byte, overflow bool) usageMeasurement {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if mediaType == "text/event-stream" {
		return parseSSEUsageMeasurement(state, overflow)
	}
	if mediaType != "" && mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json") {
		return usageMeasurement{CaptureStatus: domain.UsageCaptureUnavailable}
	}
	if overflow {
		return parseJSONUsageTail(state)
	}
	return parseJSONUsageMeasurement(state)
}

func parseJSONUsageTail(state []byte) usageMeasurement {
	keyAt := bytes.LastIndex(state, []byte(`"usage"`))
	if keyAt < 0 {
		return usageMeasurement{CaptureStatus: domain.UsageCaptureParseError, ErrorCategory: "parser_limit"}
	}
	afterKey := state[keyAt+len(`"usage"`):]
	colonAt := bytes.IndexByte(afterKey, ':')
	if colonAt < 0 {
		return usageMeasurement{CaptureStatus: domain.UsageCaptureParseError, ErrorCategory: "invalid_usage"}
	}
	decoder := json.NewDecoder(bytes.NewReader(afterKey[colonAt+1:]))
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return usageMeasurement{CaptureStatus: domain.UsageCaptureParseError, ErrorCategory: "parser_limit"}
	}
	envelope := append([]byte(`{"usage":`), raw...)
	envelope = append(envelope, '}')
	return parseJSONUsageMeasurement(envelope)
}

func parseSSEUsageMeasurement(state []byte, overflow bool) usageMeasurement {
	result := usageMeasurement{CaptureStatus: domain.UsageCaptureUnavailable}
	parseFailure := usageMeasurement{CaptureStatus: domain.UsageCaptureUnavailable}
	for _, line := range bytes.Split(state, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		candidate := parseJSONUsageMeasurement(payload)
		switch candidate.CaptureStatus {
		case domain.UsageCaptureReported, domain.UsageCapturePartial:
			result = candidate
		case domain.UsageCaptureParseError:
			parseFailure = candidate
		}
	}
	if result.CaptureStatus == domain.UsageCaptureReported ||
		result.CaptureStatus == domain.UsageCapturePartial {
		return result
	}
	if parseFailure.CaptureStatus == domain.UsageCaptureParseError {
		return parseFailure
	}
	if overflow {
		return usageMeasurement{
			CaptureStatus: domain.UsageCaptureParseError,
			ErrorCategory: "parser_limit",
		}
	}
	return result
}

func parseJSONUsageMeasurement(state []byte) usageMeasurement {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimSpace(state), &envelope); err != nil {
		return usageMeasurement{CaptureStatus: domain.UsageCaptureParseError, ErrorCategory: "invalid_json"}
	}
	raw, exists := envelope["usage"]
	if !exists || len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return usageMeasurement{CaptureStatus: domain.UsageCaptureUnavailable}
	}
	var usage map[string]json.RawMessage
	if err := json.Unmarshal(raw, &usage); err != nil {
		return usageMeasurement{CaptureStatus: domain.UsageCaptureParseError, ErrorCategory: "invalid_usage"}
	}
	input, inputPresent, inputErr := usageInt(usage, "input_tokens", "prompt_tokens")
	output, outputPresent, outputErr := usageInt(usage, "output_tokens", "completion_tokens")
	cacheRead, cacheReadPresent, cacheReadErr := usageInt(usage, "cache_read_input_tokens")
	cacheWrite, cacheWritePresent, cacheWriteErr := usageInt(usage, "cache_creation_input_tokens", "cache_write_input_tokens")
	if !cacheReadPresent {
		cacheRead, cacheReadPresent, cacheReadErr = usageDetailInt(usage,
			[]string{"input_tokens_details", "prompt_tokens_details"}, "cached_tokens")
	}
	if !cacheWritePresent {
		cacheWrite, cacheWritePresent, cacheWriteErr = usageDetailInt(usage,
			[]string{"input_tokens_details", "prompt_tokens_details"}, "cache_write_tokens")
	}
	if inputErr != nil || outputErr != nil || cacheReadErr != nil || cacheWriteErr != nil {
		return usageMeasurement{CaptureStatus: domain.UsageCaptureParseError, ErrorCategory: "invalid_usage_value"}
	}
	reportedCost, costPresent, costErr := usageDecimalMicros(usage, "total_cost", "cost")
	reportedCurrency, currencyPresent, currencyErr := usageString(usage, "currency")
	if costErr != nil || currencyErr != nil {
		return usageMeasurement{CaptureStatus: domain.UsageCaptureParseError, ErrorCategory: "invalid_cost_value"}
	}
	reportedCurrency = strings.ToUpper(strings.TrimSpace(reportedCurrency))
	if costPresent && (!currencyPresent || !validUsageCurrency(reportedCurrency)) {
		// A currency is part of the provider-reported fact. Never invent USD.
		costPresent = false
	}
	measurement := usageMeasurement{
		InputTokens:        pointerIfPresent(input, inputPresent),
		OutputTokens:       pointerIfPresent(output, outputPresent),
		CacheReadTokens:    pointerIfPresent(cacheRead, cacheReadPresent),
		CacheWriteTokens:   pointerIfPresent(cacheWrite, cacheWritePresent),
		ReportedCostMicros: pointerIfPresent(reportedCost, costPresent),
		ReportedCurrency:   reportedCurrency,
		CaptureStatus:      domain.UsageCapturePartial,
	}
	if inputPresent && outputPresent {
		measurement.CaptureStatus = domain.UsageCaptureReported
	}
	return measurement
}

func usageDecimalMicros(values map[string]json.RawMessage, keys ...string) (int64, bool, error) {
	for _, key := range keys {
		raw, ok := values[key]
		if !ok {
			continue
		}
		value := strings.TrimSpace(string(raw))
		if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
			var decoded string
			if err := json.Unmarshal(raw, &decoded); err != nil {
				return 0, true, err
			}
			value = decoded
		}
		rational, ok := new(big.Rat).SetString(value)
		if !ok || rational.Sign() < 0 {
			return 0, true, errors.New("cost must be a non-negative decimal")
		}
		scaled := new(big.Int).Mul(rational.Num(), big.NewInt(1_000_000))
		quotient, remainder := new(big.Int), new(big.Int)
		quotient.QuoRem(scaled, rational.Denom(), remainder)
		if new(big.Int).Lsh(remainder, 1).Cmp(rational.Denom()) >= 0 {
			quotient.Add(quotient, big.NewInt(1))
		}
		if !quotient.IsInt64() {
			return 0, true, errors.New("cost is too large")
		}
		return quotient.Int64(), true, nil
	}
	return 0, false, nil
}

func usageString(values map[string]json.RawMessage, key string) (string, bool, error) {
	raw, ok := values[key]
	if !ok {
		return "", false, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", true, err
	}
	return value, true, nil
}

func validUsageCurrency(value string) bool {
	if len(value) < 3 || len(value) > 8 {
		return false
	}
	for _, char := range value {
		if char < 'A' || char > 'Z' {
			return false
		}
	}
	return true
}

func usageInt(values map[string]json.RawMessage, keys ...string) (int64, bool, error) {
	for _, key := range keys {
		raw, ok := values[key]
		if !ok {
			continue
		}
		var value int64
		if err := json.Unmarshal(raw, &value); err != nil || value < 0 {
			return 0, true, errors.New("usage value must be a non-negative integer")
		}
		return value, true, nil
	}
	return 0, false, nil
}

func usageDetailInt(values map[string]json.RawMessage, parents []string, key string) (int64, bool, error) {
	for _, parent := range parents {
		raw, ok := values[parent]
		if !ok {
			continue
		}
		var details map[string]json.RawMessage
		if err := json.Unmarshal(raw, &details); err != nil {
			return 0, true, err
		}
		return usageInt(details, key)
	}
	return 0, false, nil
}

func pointerIfPresent(value int64, present bool) *int64 {
	if !present {
		return nil
	}
	return &value
}

func (s *Server) submitUsageEvent(event domain.UsageEvent) {
	select {
	case s.usageWriteSlots <- struct{}{}:
		go func() {
			defer func() { <-s.usageWriteSlots }()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if event.SubjectKind == domain.UsageSubjectRun {
				dimensions, err := s.getRunUsageDimensionsWithRetry(ctx, event.SubjectID)
				if err != nil {
					s.log.Warn("usage attribution lookup failed",
						"subject_kind", event.SubjectKind, "subject", event.SubjectID, "category", "run_dimensions")
				} else {
					if dimensions.ProjectID != "" {
						event.ProjectID = dimensions.ProjectID
					}
					if dimensions.ServiceID != "" {
						event.ServiceID = dimensions.ServiceID
					}
					event.ProjectName = dimensions.ProjectName
					event.ServiceName = dimensions.ServiceName
					event.AutomationID, event.AutomationName = dimensions.AutomationID, dimensions.AutomationName
					event.CardWorkspace, event.CardDocumentID, event.CardPath =
						dimensions.CardWorkspace, dimensions.CardDocumentID, dimensions.CardPath
					event.AccountableUserID, event.AccountableLabel =
						dimensions.AccountableUserID, dimensions.AccountableLabel
				}
				if event.ProjectID == "" {
					s.log.Warn("usage capture missing required Run attribution",
						"subject_kind", event.SubjectKind, "subject", event.SubjectID, "category", "run_project")
					return
				}
			} else if event.SubjectKind == domain.UsageSubjectDevice {
				if device, err := s.st.GetDevice(ctx, event.SubjectID); err == nil {
					event.DeviceName = device.Name
				} else if !errors.Is(err, store.ErrNotFound) {
					s.log.Warn("usage device snapshot lookup failed",
						"subject_kind", event.SubjectKind, "subject", event.SubjectID, "category", "device")
				}
			}
			if event.ProviderID != "" {
				if provider, err := s.st.GetModelProvider(ctx, event.ProviderID); err == nil {
					event.ProviderName = provider.Name
					event.ProviderKind = provider.Kind
				} else if !errors.Is(err, store.ErrNotFound) {
					s.log.Warn("usage provider snapshot lookup failed",
						"subject_kind", event.SubjectKind, "subject", event.SubjectID, "category", "provider")
				}
			}
			if err := s.applyUsagePricing(ctx, &event); err != nil {
				s.log.Warn("usage pricing lookup failed",
					"subject_kind", event.SubjectKind, "subject", event.SubjectID, "category", "pricing")
			}
			if _, err := s.st.RecordUsageEvent(ctx, &event); err != nil {
				s.log.Warn("usage capture persistence failed",
					"subject_kind", event.SubjectKind, "subject", event.SubjectID, "category", "store")
			}
		}()
	default:
		s.log.Warn("usage capture queue full",
			"subject_kind", event.SubjectKind, "subject", event.SubjectID, "category", "queue_full")
	}
}

func (s *Server) getRunUsageDimensionsWithRetry(
	ctx context.Context,
	runID string,
) (domain.RunUsageDimensions, error) {
	var lastErr error
	for attempt, delay := range []time.Duration{0, 25 * time.Millisecond, 75 * time.Millisecond} {
		if attempt > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return domain.RunUsageDimensions{}, ctx.Err()
			case <-timer.C:
			}
		}
		dimensions, err := s.st.GetRunUsageDimensions(ctx, runID)
		if err == nil {
			return dimensions, nil
		}
		lastErr = err
	}
	return domain.RunUsageDimensions{}, lastErr
}

func (s *Server) applyUsagePricing(ctx context.Context, event *domain.UsageEvent) error {
	markUncosted := func() {
		input := usageOrdinaryInput(event)
		if event.InputTokens != nil {
			event.UncostedInputTokens = input
		}
		if event.OutputTokens != nil {
			event.UncostedOutputTokens = *event.OutputTokens
		}
		if event.CacheReadTokens != nil {
			event.UncostedCacheReadTokens = *event.CacheReadTokens
		}
		if event.CacheWriteTokens != nil {
			event.UncostedCacheWriteTokens = *event.CacheWriteTokens
		}
	}
	if event.ModelID == "" {
		markUncosted()
		return nil
	}
	revision, err := s.st.ResolveModelPricingRevision(ctx, event.ModelID, event.OccurredAt)
	if errors.Is(err, store.ErrNotFound) {
		markUncosted()
		return nil
	}
	if err != nil {
		markUncosted()
		return err
	}
	var estimated int64
	hasEstimate := false
	price := func(tokens int64, rate *int64, uncosted *int64) {
		if tokens <= 0 {
			return
		}
		if rate == nil {
			*uncosted += tokens
			return
		}
		estimated += usagePriceMicros(tokens, *rate)
		hasEstimate = true
	}
	price(usageOrdinaryInput(event), revision.InputMicrosPerMillion, &event.UncostedInputTokens)
	if event.OutputTokens != nil {
		price(*event.OutputTokens, revision.OutputMicrosPerMillion, &event.UncostedOutputTokens)
	}
	if event.CacheReadTokens != nil {
		price(*event.CacheReadTokens, revision.CacheReadMicrosPerMillion, &event.UncostedCacheReadTokens)
	}
	if event.CacheWriteTokens != nil {
		price(*event.CacheWriteTokens, revision.CacheWriteMicrosPerMillion, &event.UncostedCacheWriteTokens)
	}
	event.PricingRevisionID = revision.ID
	if hasEstimate {
		event.EstimatedCostMicros = &estimated
		event.EstimatedCurrency = revision.Currency
	}
	return nil
}

func usageOrdinaryInput(event *domain.UsageEvent) int64 {
	if event.InputTokens == nil {
		return 0
	}
	value := *event.InputTokens
	if event.CacheReadTokens != nil {
		value -= *event.CacheReadTokens
	}
	if event.CacheWriteTokens != nil {
		value -= *event.CacheWriteTokens
	}
	if value < 0 {
		return 0
	}
	return value
}

func usagePriceMicros(tokens, microsPerMillion int64) int64 {
	numerator := new(big.Int).Mul(big.NewInt(tokens), big.NewInt(microsPerMillion))
	quotient, remainder := new(big.Int), new(big.Int)
	denominator := big.NewInt(1_000_000)
	quotient.QuoRem(numerator, denominator, remainder)
	if new(big.Int).Lsh(remainder, 1).Cmp(denominator) >= 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsInt64() {
		return int64(^uint64(0) >> 1)
	}
	return quotient.Int64()
}
