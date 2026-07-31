package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/cnjack/jcloud/internal/domain"
)

func (m *MemStore) RecordUsageEvent(_ context.Context, event *domain.UsageEvent) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.usageReceipts[event.RequestID]; exists {
		return false, nil
	}
	m.usageReceipts[event.RequestID] = event.OccurredAt
	m.usageEvents[event.RequestID] = *event
	m.addUsageRollupLocked(*event)
	return true, nil
}

type usageRollup struct {
	BucketAt time.Time
	Event    domain.UsageEvent
	Requests int64
	Capture  domain.UsageCaptureCounts
}

func usageRollupKey(event domain.UsageEvent) string {
	return strings.Join([]string{
		event.OccurredAt.UTC().Truncate(time.Hour).Format(time.RFC3339),
		string(event.SubjectKind), event.SubjectID, event.RunID,
		event.ProjectID, event.ServiceID, event.AutomationID,
		event.CardWorkspace, event.CardDocumentID, event.CardPath,
		event.AccountableUserID, event.UserID, event.DeviceID,
		event.GrantScope, event.GrantScopeID, event.ProviderID, event.ModelID,
		event.ReportedCurrency, event.PricingRevisionID, event.EstimatedCurrency,
	}, "\x00")
}

func addUsageValue(target **int64, value *int64) {
	if value == nil {
		return
	}
	if *target == nil {
		copyValue := *value
		*target = &copyValue
		return
	}
	**target += *value
}

func (m *MemStore) addUsageRollupLocked(event domain.UsageEvent) {
	key := usageRollupKey(event)
	value := m.usageRollups[key]
	if value.Requests == 0 {
		value.BucketAt = event.OccurredAt.UTC().Truncate(time.Hour)
		value.Event = event
		value.Event.OccurredAt = value.BucketAt
	} else {
		addUsageValue(&value.Event.InputTokens, event.InputTokens)
		addUsageValue(&value.Event.OutputTokens, event.OutputTokens)
		addUsageValue(&value.Event.CacheReadTokens, event.CacheReadTokens)
		addUsageValue(&value.Event.CacheWriteTokens, event.CacheWriteTokens)
		addUsageValue(&value.Event.ReportedCostMicros, event.ReportedCostMicros)
		addUsageValue(&value.Event.EstimatedCostMicros, event.EstimatedCostMicros)
		value.Event.UncostedInputTokens += event.UncostedInputTokens
		value.Event.UncostedOutputTokens += event.UncostedOutputTokens
		value.Event.UncostedCacheReadTokens += event.UncostedCacheReadTokens
		value.Event.UncostedCacheWriteTokens += event.UncostedCacheWriteTokens
	}
	value.Requests++
	switch event.CaptureStatus {
	case domain.UsageCaptureReported:
		value.Capture.Reported++
	case domain.UsageCapturePartial:
		value.Capture.Partial++
	case domain.UsageCaptureUnavailable:
		value.Capture.Unavailable++
	case domain.UsageCaptureParseError:
		value.Capture.ParseError++
	}
	m.usageRollups[key] = value
}

func (m *MemStore) GetUsageSummary(_ context.Context, query domain.UsageSummaryQuery) (domain.UsageSummary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rollups := make([]usageRollup, 0)
	for _, value := range m.usageRollups {
		if usageEventMatches(value.Event, query) {
			rollups = append(rollups, value)
		}
	}
	return summarizeUsageRollups(rollups, query.From, query.To), nil
}

const usageEventCols = `id,request_id,subject_kind,subject_id,run_id,
	project_id,project_name,service_id,service_name,
	automation_id,automation_name,card_workspace,card_document_id,card_path,
	accountable_user_id,accountable_label,
	user_id,device_id,device_name,grant_scope,grant_scope_id,grant_scope_name,
	provider_id,provider_kind,provider_name,model_id,model_name,
	input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,
	reported_cost_micros,reported_currency,pricing_revision_id,
	estimated_cost_micros,estimated_currency,
	uncosted_input_tokens,uncosted_output_tokens,uncosted_cache_read_tokens,uncosted_cache_write_tokens,
	capture_status,error_category,occurred_at,created_at,replacement_of,version`

func (s *PGStore) RecordUsageEvent(ctx context.Context, event *domain.UsageEvent) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("record usage event: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	tag, err := tx.Exec(ctx, `INSERT INTO usage_request_receipts (request_id,occurred_at)
		VALUES ($1,$2) ON CONFLICT (request_id) DO NOTHING`, event.RequestID, event.OccurredAt)
	if err != nil {
		return false, fmt.Errorf("record usage receipt: %w", err)
	}
	if tag.RowsAffected() == 0 {
		if err = tx.Commit(ctx); err != nil {
			return false, fmt.Errorf("record duplicate usage event: commit: %w", err)
		}
		return false, nil
	}
	_, err = tx.Exec(ctx, `INSERT INTO usage_events (`+usageEventCols+`)
		VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,
			$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,$36,$37,$38,
			$39,$40,$41,$42,$43,$44,$45,$46
		)`,
		event.ID, event.RequestID, event.SubjectKind, event.SubjectID, event.RunID,
		event.ProjectID, event.ProjectName, event.ServiceID, event.ServiceName,
		event.AutomationID, event.AutomationName, event.CardWorkspace, event.CardDocumentID, event.CardPath,
		event.AccountableUserID, event.AccountableLabel,
		event.UserID, event.DeviceID, event.DeviceName, event.GrantScope, event.GrantScopeID, event.GrantScopeName,
		event.ProviderID, event.ProviderKind, event.ProviderName, event.ModelID, event.ModelName,
		event.InputTokens, event.OutputTokens, event.CacheReadTokens, event.CacheWriteTokens,
		event.ReportedCostMicros, event.ReportedCurrency, event.PricingRevisionID,
		event.EstimatedCostMicros, event.EstimatedCurrency,
		event.UncostedInputTokens, event.UncostedOutputTokens,
		event.UncostedCacheReadTokens, event.UncostedCacheWriteTokens,
		event.CaptureStatus, event.ErrorCategory, event.OccurredAt, event.CreatedAt, event.ReplacementOf, event.Version)
	if err != nil {
		return false, fmt.Errorf("record usage event: %w", err)
	}
	reported, partial, unavailable, parseError := int64(0), int64(0), int64(0), int64(0)
	switch event.CaptureStatus {
	case domain.UsageCaptureReported:
		reported = 1
	case domain.UsageCapturePartial:
		partial = 1
	case domain.UsageCaptureUnavailable:
		unavailable = 1
	case domain.UsageCaptureParseError:
		parseError = 1
	}
	bucket := event.OccurredAt.UTC().Truncate(time.Hour)
	_, err = tx.Exec(ctx, `INSERT INTO usage_hourly_rollups (
		bucket_at,subject_kind,subject_id,run_id,
		project_id,project_name,service_id,service_name,
		automation_id,automation_name,card_workspace,card_document_id,card_path,
		accountable_user_id,accountable_label,user_id,device_id,device_name,
		grant_scope,grant_scope_id,grant_scope_name,
		provider_id,provider_kind,provider_name,model_id,model_name,
		reported_currency,pricing_revision_id,estimated_currency,
		requests,reported_count,partial_count,unavailable_count,parse_error_count,
		input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,
		reported_cost_micros,estimated_cost_micros,
		uncosted_input_tokens,uncosted_output_tokens,
		uncosted_cache_read_tokens,uncosted_cache_write_tokens
	) VALUES (
		$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,
		$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,$36,$37,$38,
		$39,$40,$41,$42,$43,$44
	)
	ON CONFLICT (
		bucket_at,subject_kind,subject_id,run_id,project_id,service_id,
		automation_id,card_workspace,card_document_id,card_path,
		accountable_user_id,user_id,device_id,grant_scope,grant_scope_id,
		provider_id,model_id,reported_currency,pricing_revision_id,estimated_currency
	) DO UPDATE SET
		requests=usage_hourly_rollups.requests+1,
		reported_count=usage_hourly_rollups.reported_count+EXCLUDED.reported_count,
		partial_count=usage_hourly_rollups.partial_count+EXCLUDED.partial_count,
		unavailable_count=usage_hourly_rollups.unavailable_count+EXCLUDED.unavailable_count,
		parse_error_count=usage_hourly_rollups.parse_error_count+EXCLUDED.parse_error_count,
		input_tokens=CASE WHEN EXCLUDED.input_tokens IS NULL THEN usage_hourly_rollups.input_tokens
			ELSE COALESCE(usage_hourly_rollups.input_tokens,0)+EXCLUDED.input_tokens END,
		output_tokens=CASE WHEN EXCLUDED.output_tokens IS NULL THEN usage_hourly_rollups.output_tokens
			ELSE COALESCE(usage_hourly_rollups.output_tokens,0)+EXCLUDED.output_tokens END,
		cache_read_tokens=CASE WHEN EXCLUDED.cache_read_tokens IS NULL THEN usage_hourly_rollups.cache_read_tokens
			ELSE COALESCE(usage_hourly_rollups.cache_read_tokens,0)+EXCLUDED.cache_read_tokens END,
		cache_write_tokens=CASE WHEN EXCLUDED.cache_write_tokens IS NULL THEN usage_hourly_rollups.cache_write_tokens
			ELSE COALESCE(usage_hourly_rollups.cache_write_tokens,0)+EXCLUDED.cache_write_tokens END,
		reported_cost_micros=CASE WHEN EXCLUDED.reported_cost_micros IS NULL THEN usage_hourly_rollups.reported_cost_micros
			ELSE COALESCE(usage_hourly_rollups.reported_cost_micros,0)+EXCLUDED.reported_cost_micros END,
		estimated_cost_micros=CASE WHEN EXCLUDED.estimated_cost_micros IS NULL THEN usage_hourly_rollups.estimated_cost_micros
			ELSE COALESCE(usage_hourly_rollups.estimated_cost_micros,0)+EXCLUDED.estimated_cost_micros END,
		uncosted_input_tokens=usage_hourly_rollups.uncosted_input_tokens+EXCLUDED.uncosted_input_tokens,
		uncosted_output_tokens=usage_hourly_rollups.uncosted_output_tokens+EXCLUDED.uncosted_output_tokens,
		uncosted_cache_read_tokens=usage_hourly_rollups.uncosted_cache_read_tokens+EXCLUDED.uncosted_cache_read_tokens,
		uncosted_cache_write_tokens=usage_hourly_rollups.uncosted_cache_write_tokens+EXCLUDED.uncosted_cache_write_tokens`,
		bucket, event.SubjectKind, event.SubjectID, event.RunID,
		event.ProjectID, event.ProjectName, event.ServiceID, event.ServiceName,
		event.AutomationID, event.AutomationName, event.CardWorkspace, event.CardDocumentID, event.CardPath,
		event.AccountableUserID, event.AccountableLabel, event.UserID, event.DeviceID, event.DeviceName,
		event.GrantScope, event.GrantScopeID, event.GrantScopeName,
		event.ProviderID, event.ProviderKind, event.ProviderName, event.ModelID, event.ModelName,
		event.ReportedCurrency, event.PricingRevisionID, event.EstimatedCurrency,
		int64(1), reported, partial, unavailable, parseError,
		event.InputTokens, event.OutputTokens, event.CacheReadTokens, event.CacheWriteTokens,
		event.ReportedCostMicros, event.EstimatedCostMicros,
		event.UncostedInputTokens, event.UncostedOutputTokens,
		event.UncostedCacheReadTokens, event.UncostedCacheWriteTokens)
	if err != nil {
		return false, fmt.Errorf("record usage rollup: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("record usage event: commit: %w", err)
	}
	return true, nil
}

func (s *PGStore) GetUsageSummary(ctx context.Context, query domain.UsageSummaryQuery) (domain.UsageSummary, error) {
	where, args := usageQueryWhere(query, "bucket_at")
	rows, err := s.pool.Query(ctx, `SELECT `+usageRollupCols+`
		FROM usage_hourly_rollups`+where+` ORDER BY bucket_at`, args...)
	if err != nil {
		return domain.UsageSummary{}, fmt.Errorf("query usage rollups: %w", err)
	}
	defer rows.Close()
	rollups := make([]usageRollup, 0)
	for rows.Next() {
		value, scanErr := scanUsageRollup(rows)
		if scanErr != nil {
			return domain.UsageSummary{}, scanErr
		}
		rollups = append(rollups, *value)
	}
	if err = rows.Err(); err != nil {
		return domain.UsageSummary{}, err
	}
	return summarizeUsageRollups(rollups, query.From, query.To), nil
}

const usageRollupCols = `bucket_at,subject_kind,subject_id,run_id,
	project_id,project_name,service_id,service_name,
	automation_id,automation_name,card_workspace,card_document_id,card_path,
	accountable_user_id,accountable_label,user_id,device_id,device_name,
	grant_scope,grant_scope_id,grant_scope_name,
	provider_id,provider_kind,provider_name,model_id,model_name,
	reported_currency,pricing_revision_id,estimated_currency,
	requests,reported_count,partial_count,unavailable_count,parse_error_count,
	input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,
	reported_cost_micros,estimated_cost_micros,
	uncosted_input_tokens,uncosted_output_tokens,
	uncosted_cache_read_tokens,uncosted_cache_write_tokens`

func scanUsageRollup(row pgx.Row) (*usageRollup, error) {
	var value usageRollup
	event := &value.Event
	err := row.Scan(
		&value.BucketAt, &event.SubjectKind, &event.SubjectID, &event.RunID,
		&event.ProjectID, &event.ProjectName, &event.ServiceID, &event.ServiceName,
		&event.AutomationID, &event.AutomationName, &event.CardWorkspace, &event.CardDocumentID, &event.CardPath,
		&event.AccountableUserID, &event.AccountableLabel, &event.UserID, &event.DeviceID, &event.DeviceName,
		&event.GrantScope, &event.GrantScopeID, &event.GrantScopeName,
		&event.ProviderID, &event.ProviderKind, &event.ProviderName, &event.ModelID, &event.ModelName,
		&event.ReportedCurrency, &event.PricingRevisionID, &event.EstimatedCurrency,
		&value.Requests, &value.Capture.Reported, &value.Capture.Partial,
		&value.Capture.Unavailable, &value.Capture.ParseError,
		&event.InputTokens, &event.OutputTokens, &event.CacheReadTokens, &event.CacheWriteTokens,
		&event.ReportedCostMicros, &event.EstimatedCostMicros,
		&event.UncostedInputTokens, &event.UncostedOutputTokens,
		&event.UncostedCacheReadTokens, &event.UncostedCacheWriteTokens)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan usage rollup: %w", err)
	}
	event.OccurredAt = value.BucketAt
	return &value, nil
}

func usageQueryWhere(query domain.UsageSummaryQuery, timeColumn string) (string, []any) {
	where := ` WHERE 1=1`
	args := make([]any, 0, 12)
	add := func(column string, value any) {
		args = append(args, value)
		where += fmt.Sprintf(" AND %s=$%d", column, len(args))
	}
	if query.SubjectKind != "" {
		add("subject_kind", query.SubjectKind)
	}
	if query.SubjectID != "" {
		add("subject_id", query.SubjectID)
	}
	if query.RunID != "" {
		add("run_id", query.RunID)
	}
	if query.ProjectID != "" {
		add("project_id", query.ProjectID)
	}
	if query.ServiceID != "" {
		add("service_id", query.ServiceID)
	}
	if query.AutomationID != "" {
		add("automation_id", query.AutomationID)
	}
	if query.CardWorkspace != "" {
		add("card_workspace", query.CardWorkspace)
	}
	if query.CardPath != "" {
		add("card_path", query.CardPath)
	}
	if query.UserID != "" {
		add("user_id", query.UserID)
	}
	if query.DeviceID != "" {
		add("device_id", query.DeviceID)
	}
	if query.ModelID != "" {
		add("model_id", query.ModelID)
	}
	if query.GrantScope != "" {
		add("grant_scope", query.GrantScope)
	}
	if query.GrantScopeID != "" {
		add("grant_scope_id", query.GrantScopeID)
	}
	if query.From != nil {
		args = append(args, query.From.UTC().Truncate(time.Hour))
		where += fmt.Sprintf(" AND %s >= $%d", timeColumn, len(args))
	}
	if query.To != nil {
		args = append(args, *query.To)
		where += fmt.Sprintf(" AND %s < $%d", timeColumn, len(args))
	}
	return where, args
}

func scanUsageEvent(row pgx.Row) (*domain.UsageEvent, error) {
	var event domain.UsageEvent
	err := row.Scan(
		&event.ID, &event.RequestID, &event.SubjectKind, &event.SubjectID, &event.RunID,
		&event.ProjectID, &event.ProjectName, &event.ServiceID, &event.ServiceName,
		&event.AutomationID, &event.AutomationName, &event.CardWorkspace, &event.CardDocumentID, &event.CardPath,
		&event.AccountableUserID, &event.AccountableLabel,
		&event.UserID, &event.DeviceID, &event.DeviceName, &event.GrantScope, &event.GrantScopeID, &event.GrantScopeName,
		&event.ProviderID, &event.ProviderKind, &event.ProviderName, &event.ModelID, &event.ModelName,
		&event.InputTokens, &event.OutputTokens, &event.CacheReadTokens, &event.CacheWriteTokens,
		&event.ReportedCostMicros, &event.ReportedCurrency, &event.PricingRevisionID,
		&event.EstimatedCostMicros, &event.EstimatedCurrency,
		&event.UncostedInputTokens, &event.UncostedOutputTokens,
		&event.UncostedCacheReadTokens, &event.UncostedCacheWriteTokens,
		&event.CaptureStatus, &event.ErrorCategory, &event.OccurredAt, &event.CreatedAt, &event.ReplacementOf, &event.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan usage event: %w", err)
	}
	return &event, nil
}

func usageEventMatches(event domain.UsageEvent, query domain.UsageSummaryQuery) bool {
	if query.SubjectKind != "" && event.SubjectKind != query.SubjectKind ||
		query.SubjectID != "" && event.SubjectID != query.SubjectID ||
		query.RunID != "" && event.RunID != query.RunID ||
		query.ProjectID != "" && event.ProjectID != query.ProjectID ||
		query.ServiceID != "" && event.ServiceID != query.ServiceID ||
		query.AutomationID != "" && event.AutomationID != query.AutomationID ||
		query.CardWorkspace != "" && event.CardWorkspace != query.CardWorkspace ||
		query.CardPath != "" && event.CardPath != query.CardPath ||
		query.UserID != "" && event.UserID != query.UserID ||
		query.DeviceID != "" && event.DeviceID != query.DeviceID ||
		query.ModelID != "" && event.ModelID != query.ModelID ||
		query.GrantScope != "" && event.GrantScope != query.GrantScope ||
		query.GrantScopeID != "" && event.GrantScopeID != query.GrantScopeID {
		return false
	}
	if query.From != nil && event.OccurredAt.Before(query.From.UTC().Truncate(time.Hour)) {
		return false
	}
	return query.To == nil || event.OccurredAt.Before(*query.To)
}

func summarizeUsageEvents(events []domain.UsageEvent, from, to *time.Time) domain.UsageSummary {
	rollups := make([]usageRollup, 0, len(events))
	for _, event := range events {
		value := usageRollup{Event: event, Requests: 1}
		switch event.CaptureStatus {
		case domain.UsageCaptureReported:
			value.Capture.Reported = 1
		case domain.UsageCapturePartial:
			value.Capture.Partial = 1
		case domain.UsageCaptureUnavailable:
			value.Capture.Unavailable = 1
		case domain.UsageCaptureParseError:
			value.Capture.ParseError = 1
		}
		rollups = append(rollups, value)
	}
	return summarizeUsageRollups(rollups, from, to)
}

func summarizeUsageRollups(rollups []usageRollup, from, to *time.Time) domain.UsageSummary {
	summary := domain.UsageSummary{
		Availability: "unavailable",
		Reason:       "no_requests",
		Costs: domain.UsageCostTotals{
			Reported:  []domain.UsageMoneyTotal{},
			Estimated: []domain.UsageMoneyTotal{},
			Uncosted:  []domain.UsageUncostedTotal{},
		},
		From: from, To: to,
	}
	if len(rollups) == 0 {
		return summary
	}
	summary.Reason = "usage_not_reported"
	var input, output, cacheRead, cacheWrite int64
	var hasInput, hasOutput, hasCacheRead, hasCacheWrite bool
	reported := map[string]int64{}
	estimated := map[string]int64{}
	revisions := map[string]map[string]bool{}
	for _, rollup := range rollups {
		event := rollup.Event
		summary.Requests += rollup.Requests
		summary.Capture.Reported += rollup.Capture.Reported
		summary.Capture.Partial += rollup.Capture.Partial
		summary.Capture.Unavailable += rollup.Capture.Unavailable
		summary.Capture.ParseError += rollup.Capture.ParseError
		if event.InputTokens != nil {
			input += *event.InputTokens
			hasInput = true
		}
		if event.OutputTokens != nil {
			output += *event.OutputTokens
			hasOutput = true
		}
		if event.CacheReadTokens != nil {
			cacheRead += *event.CacheReadTokens
			hasCacheRead = true
		}
		if event.CacheWriteTokens != nil {
			cacheWrite += *event.CacheWriteTokens
			hasCacheWrite = true
		}
		if event.ReportedCostMicros != nil && event.ReportedCurrency != "" {
			reported[event.ReportedCurrency] += *event.ReportedCostMicros
		}
		if event.EstimatedCostMicros != nil && event.EstimatedCurrency != "" {
			estimated[event.EstimatedCurrency] += *event.EstimatedCostMicros
			if revisions[event.EstimatedCurrency] == nil {
				revisions[event.EstimatedCurrency] = map[string]bool{}
			}
			if event.PricingRevisionID != "" {
				revisions[event.EstimatedCurrency][event.PricingRevisionID] = true
			}
		}
		addUncosted := func(category string, tokens int64) {
			if tokens <= 0 {
				return
			}
			for i := range summary.Costs.Uncosted {
				if summary.Costs.Uncosted[i].Category == category {
					summary.Costs.Uncosted[i].Tokens += tokens
					return
				}
			}
			summary.Costs.Uncosted = append(summary.Costs.Uncosted,
				domain.UsageUncostedTotal{Category: category, Tokens: tokens})
		}
		addUncosted("input", event.UncostedInputTokens)
		addUncosted("output", event.UncostedOutputTokens)
		addUncosted("cache_read", event.UncostedCacheReadTokens)
		addUncosted("cache_write", event.UncostedCacheWriteTokens)
	}
	if hasInput || hasOutput || hasCacheRead || hasCacheWrite ||
		len(reported) > 0 || len(estimated) > 0 || len(summary.Costs.Uncosted) > 0 {
		summary.Availability = "available"
		summary.Reason = ""
	}
	if hasInput {
		summary.Tokens.Input = &input
	}
	if hasOutput {
		summary.Tokens.Output = &output
	}
	if hasCacheRead {
		summary.Tokens.CacheRead = &cacheRead
	}
	if hasCacheWrite {
		summary.Tokens.CacheWrite = &cacheWrite
	}
	for currency, micros := range reported {
		summary.Costs.Reported = append(summary.Costs.Reported, domain.UsageMoneyTotal{Currency: currency, Micros: micros})
	}
	for currency, micros := range estimated {
		ids := make([]string, 0, len(revisions[currency]))
		for id := range revisions[currency] {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		summary.Costs.Estimated = append(summary.Costs.Estimated, domain.UsageMoneyTotal{
			Currency: currency, Micros: micros, PricingRevisionIDs: ids,
		})
	}
	sort.Slice(summary.Costs.Reported, func(i, j int) bool { return summary.Costs.Reported[i].Currency < summary.Costs.Reported[j].Currency })
	sort.Slice(summary.Costs.Estimated, func(i, j int) bool { return summary.Costs.Estimated[i].Currency < summary.Costs.Estimated[j].Currency })
	sort.Slice(summary.Costs.Uncosted, func(i, j int) bool { return summary.Costs.Uncosted[i].Category < summary.Costs.Uncosted[j].Category })
	return summary
}

func (m *MemStore) CleanupUsage(_ context.Context, rawBefore, rollupBefore time.Time) (int64, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var rawDeleted, rollupsDeleted int64
	for requestID, event := range m.usageEvents {
		if event.OccurredAt.Before(rawBefore) {
			delete(m.usageEvents, requestID)
			rawDeleted++
		}
	}
	for key, value := range m.usageRollups {
		if value.BucketAt.Before(rollupBefore) {
			delete(m.usageRollups, key)
			rollupsDeleted++
		}
	}
	for requestID, occurredAt := range m.usageReceipts {
		if occurredAt.Before(rollupBefore) {
			delete(m.usageReceipts, requestID)
		}
	}
	return rawDeleted, rollupsDeleted, nil
}

func (s *PGStore) CleanupUsage(ctx context.Context, rawBefore, rollupBefore time.Time) (int64, int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("cleanup usage: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	rawTag, err := tx.Exec(ctx, `DELETE FROM usage_events WHERE occurred_at < $1`, rawBefore)
	if err != nil {
		return 0, 0, fmt.Errorf("cleanup usage events: %w", err)
	}
	rollupTag, err := tx.Exec(ctx, `DELETE FROM usage_hourly_rollups WHERE bucket_at < $1`, rollupBefore)
	if err != nil {
		return 0, 0, fmt.Errorf("cleanup usage rollups: %w", err)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM usage_request_receipts WHERE occurred_at < $1`, rollupBefore); err != nil {
		return 0, 0, fmt.Errorf("cleanup usage receipts: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, 0, fmt.Errorf("cleanup usage: commit: %w", err)
	}
	return rawTag.RowsAffected(), rollupTag.RowsAffected(), nil
}

func (m *MemStore) CreateModelPricingRevision(_ context.Context, revision *domain.ModelPricingRevision) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.models[revision.ModelResourceID]; !ok {
		return ErrNotFound
	}
	if _, exists := m.modelPricingRevisions[revision.ID]; exists {
		return ErrAlreadyExists
	}
	m.modelPricingRevisions[revision.ID] = *revision
	return nil
}

func (m *MemStore) ListModelPricingRevisions(_ context.Context, modelID string) ([]domain.ModelPricingRevision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.models[modelID]; !ok {
		return nil, ErrNotFound
	}
	return m.listModelPricingRevisionsLocked(modelID), nil
}

func (m *MemStore) listModelPricingRevisionsLocked(modelID string) []domain.ModelPricingRevision {
	out := make([]domain.ModelPricingRevision, 0)
	for _, revision := range m.modelPricingRevisions {
		if revision.ModelResourceID == modelID {
			out = append(out, revision)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].EffectiveAt.Equal(out[j].EffectiveAt) {
			return out[i].EffectiveAt.After(out[j].EffectiveAt)
		}
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out
}

func (m *MemStore) ResolveModelPricingRevision(_ context.Context, modelID string, at time.Time) (*domain.ModelPricingRevision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	values := m.listModelPricingRevisionsLocked(modelID)
	for i := range values {
		if !values[i].EffectiveAt.After(at) {
			value := values[i]
			return &value, nil
		}
	}
	return nil, ErrNotFound
}

const pricingRevisionCols = `id,model_resource_id,provider_id,provider_name,model_name,currency,
	input_micros_per_million,output_micros_per_million,
	cache_read_micros_per_million,cache_write_micros_per_million,
	effective_at,created_by,created_at`

func (s *PGStore) CreateModelPricingRevision(ctx context.Context, revision *domain.ModelPricingRevision) error {
	if _, err := s.GetModel(ctx, revision.ModelResourceID); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO model_pricing_revisions (`+pricingRevisionCols+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		revision.ID, revision.ModelResourceID, revision.ProviderID, revision.ProviderName,
		revision.ModelName, revision.Currency,
		revision.InputMicrosPerMillion, revision.OutputMicrosPerMillion,
		revision.CacheReadMicrosPerMillion, revision.CacheWriteMicrosPerMillion,
		revision.EffectiveAt, revision.CreatedBy, revision.CreatedAt)
	if err != nil {
		return fmt.Errorf("create model pricing revision: %w", err)
	}
	return nil
}

func (s *PGStore) ListModelPricingRevisions(ctx context.Context, modelID string) ([]domain.ModelPricingRevision, error) {
	if _, err := s.GetModel(ctx, modelID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT `+pricingRevisionCols+`
		FROM model_pricing_revisions WHERE model_resource_id=$1
		ORDER BY effective_at DESC,created_at DESC,id DESC`, modelID)
	if err != nil {
		return nil, fmt.Errorf("list model pricing revisions: %w", err)
	}
	defer rows.Close()
	out := make([]domain.ModelPricingRevision, 0)
	for rows.Next() {
		revision, scanErr := scanModelPricingRevision(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, *revision)
	}
	return out, rows.Err()
}

func (s *PGStore) ResolveModelPricingRevision(ctx context.Context, modelID string, at time.Time) (*domain.ModelPricingRevision, error) {
	return scanModelPricingRevision(s.pool.QueryRow(ctx, `SELECT `+pricingRevisionCols+`
		FROM model_pricing_revisions
		WHERE model_resource_id=$1 AND effective_at <= $2
		ORDER BY effective_at DESC,created_at DESC,id DESC LIMIT 1`, modelID, at))
}

func scanModelPricingRevision(row pgx.Row) (*domain.ModelPricingRevision, error) {
	var revision domain.ModelPricingRevision
	err := row.Scan(&revision.ID, &revision.ModelResourceID, &revision.ProviderID,
		&revision.ProviderName, &revision.ModelName, &revision.Currency,
		&revision.InputMicrosPerMillion, &revision.OutputMicrosPerMillion,
		&revision.CacheReadMicrosPerMillion, &revision.CacheWriteMicrosPerMillion,
		&revision.EffectiveAt, &revision.CreatedBy, &revision.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan model pricing revision: %w", err)
	}
	return &revision, nil
}

func (m *MemStore) GetRunUsageDimensions(_ context.Context, runID string) (domain.RunUsageDimensions, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	run, ok := m.runs[runID]
	if !ok {
		return domain.RunUsageDimensions{}, ErrNotFound
	}
	dimensions := domain.RunUsageDimensions{
		ProjectID: run.ProjectID, ServiceID: run.ServiceID,
	}
	if project, exists := m.projects[run.ProjectID]; exists {
		dimensions.ProjectName = project.Name
	}
	if service, exists := m.services[run.ServiceID]; exists {
		dimensions.ServiceName = service.Name
	}
	if actor := run.ProvenanceSnapshot.AccountableActor; actor != nil &&
		run.ProvenanceSnapshot.Precision != "unattributed" {
		dimensions.AccountableUserID = actor.ID
		dimensions.AccountableLabel = actor.Label
	}
	if automation, exists := m.pluginAutomations[run.OriginAutomationID]; exists &&
		automation.TriggerKind != "kanban" {
		dimensions.AutomationID = automation.ID
		dimensions.AutomationName = automation.Name
		return dimensions, nil
	}
	var occurrence *domain.PluginKanbanOccurrence
	if value, exists := m.pluginKanbanOccurrences[run.OriginEventKey]; exists {
		copyValue := value
		occurrence = &copyValue
	} else {
		for _, value := range m.pluginKanbanOccurrences {
			if value.RunID == runID {
				copyValue := value
				occurrence = &copyValue
				break
			}
		}
	}
	if occurrence == nil {
		return dimensions, nil
	}
	dimensions.CardWorkspace = occurrence.WorkspaceID
	dimensions.CardDocumentID = occurrence.DocumentID
	dimensions.CardPath = occurrence.DocumentPath
	source, sourceErr := m.automationExecutionForKanbanOccurrenceLocked(occurrence.ID)
	if sourceErr == nil {
		dimensions.AutomationID = source.AutomationID
		dimensions.AutomationName = source.AutomationName
	} else if !errors.Is(sourceErr, ErrNotFound) {
		return domain.RunUsageDimensions{}, sourceErr
	}
	return dimensions, nil
}

func (s *PGStore) GetRunUsageDimensions(ctx context.Context, runID string) (domain.RunUsageDimensions, error) {
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		return domain.RunUsageDimensions{}, err
	}
	dimensions := domain.RunUsageDimensions{
		ProjectID: run.ProjectID, ServiceID: run.ServiceID,
	}
	if project, projectErr := s.GetProject(ctx, run.ProjectID); projectErr == nil {
		dimensions.ProjectName = project.Name
	} else if !errors.Is(projectErr, ErrNotFound) {
		return domain.RunUsageDimensions{}, projectErr
	}
	if service, serviceErr := s.GetService(ctx, run.ServiceID); serviceErr == nil {
		dimensions.ServiceName = service.Name
	} else if !errors.Is(serviceErr, ErrNotFound) {
		return domain.RunUsageDimensions{}, serviceErr
	}
	if actor := run.ProvenanceSnapshot.AccountableActor; actor != nil &&
		run.ProvenanceSnapshot.Precision != "unattributed" {
		dimensions.AccountableUserID = actor.ID
		dimensions.AccountableLabel = actor.Label
	}
	if run.OriginAutomationID != "" {
		if automation, automationErr := s.GetPluginAutomation(ctx, run.OriginAutomationID); automationErr == nil {
			if automation.TriggerKind != "kanban" {
				dimensions.AutomationID = automation.ID
				dimensions.AutomationName = automation.Name
				return dimensions, nil
			}
		} else if !errors.Is(automationErr, ErrNotFound) {
			return domain.RunUsageDimensions{}, automationErr
		}
	}
	occurrence, occurrenceErr := scanPluginKanbanOccurrence(s.pool.QueryRow(ctx,
		`SELECT `+pluginKanbanOccurrenceCols+`
		 FROM automation_kanban_occurrences
		 WHERE id=$1 OR run_id=$2
		 ORDER BY CASE WHEN id=$1 THEN 0 ELSE 1 END,created_at DESC,id DESC
		 LIMIT 1`, run.OriginEventKey, run.ID))
	if errors.Is(occurrenceErr, ErrNotFound) {
		return dimensions, nil
	}
	if occurrenceErr != nil {
		return domain.RunUsageDimensions{}, occurrenceErr
	}
	dimensions.CardWorkspace = occurrence.WorkspaceID
	dimensions.CardDocumentID = occurrence.DocumentID
	dimensions.CardPath = occurrence.DocumentPath
	source, sourceErr := s.GetAutomationExecutionForKanbanOccurrence(ctx, occurrence.ID)
	if sourceErr == nil {
		dimensions.AutomationID = source.AutomationID
		dimensions.AutomationName = source.AutomationName
	} else if !errors.Is(sourceErr, ErrNotFound) {
		return domain.RunUsageDimensions{}, fmt.Errorf("resolve Run usage Automation: %w", sourceErr)
	}
	return dimensions, nil
}

func (m *MemStore) ListUsageGroups(_ context.Context, query domain.UsageSummaryQuery, groupBy string) ([]domain.UsageGroup, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	grouped := map[string][]usageRollup{}
	type usageNameSnapshot struct {
		name string
		at   time.Time
	}
	nameSnapshots := map[string]usageNameSnapshot{}
	for _, value := range m.usageRollups {
		if !usageEventMatches(value.Event, query) {
			continue
		}
		id, name := usageGroupIdentity(value.Event, groupBy)
		if id == "" {
			continue
		}
		grouped[id] = append(grouped[id], value)
		current, exists := nameSnapshots[id]
		if !exists || value.BucketAt.After(current.at) ||
			value.BucketAt.Equal(current.at) && name > current.name {
			nameSnapshots[id] = usageNameSnapshot{name: name, at: value.BucketAt}
		}
	}
	names := make(map[string]string, len(nameSnapshots))
	for id, snapshot := range nameSnapshots {
		names[id] = snapshot.name
	}
	return buildUsageGroups(grouped, names, groupBy, query.From, query.To), nil
}

func (s *PGStore) ListUsageGroups(ctx context.Context, query domain.UsageSummaryQuery, groupBy string) ([]domain.UsageGroup, error) {
	var idColumn, nameColumn string
	switch groupBy {
	case "service":
		idColumn, nameColumn = "service_id", "service_name"
	case "automation":
		idColumn, nameColumn = "automation_id", "automation_name"
	case "model":
		idColumn, nameColumn = "model_id", "model_name"
	case "device":
		idColumn, nameColumn = "device_id", "device_name"
	case "grant":
		idColumn, nameColumn = "grant_scope||':'||grant_scope_id", "grant_scope_name"
	default:
		return nil, fmt.Errorf("invalid usage group %q", groupBy)
	}
	where, args := usageQueryWhere(query, "bucket_at")
	rows, err := s.pool.Query(ctx, `SELECT group_id,group_name FROM (
			SELECT `+idColumn+` AS group_id,`+nameColumn+` AS group_name,
				row_number() OVER (
					PARTITION BY `+idColumn+`
					ORDER BY bucket_at DESC,`+nameColumn+` DESC
				) AS snapshot_rank
			FROM usage_hourly_rollups`+where+` AND (`+idColumn+`)<>''
		) AS usage_group_snapshots WHERE snapshot_rank=1`, args...)
	if err != nil {
		return nil, fmt.Errorf("list usage group identities: %w", err)
	}
	defer rows.Close()
	type identity struct{ id, name string }
	identities := make([]identity, 0)
	for rows.Next() {
		var value identity
		if err := rows.Scan(&value.id, &value.name); err != nil {
			return nil, err
		}
		identities = append(identities, value)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	out := make([]domain.UsageGroup, 0, len(identities))
	for _, identity := range identities {
		child := query
		switch groupBy {
		case "service":
			child.ServiceID = identity.id
		case "automation":
			child.AutomationID = identity.id
		case "model":
			child.ModelID = identity.id
		case "device":
			child.DeviceID = identity.id
		case "grant":
			child.GrantScope, child.GrantScopeID, _ = strings.Cut(identity.id, ":")
		}
		summary, summaryErr := s.GetUsageSummary(ctx, child)
		if summaryErr != nil {
			return nil, summaryErr
		}
		out = append(out, domain.UsageGroup{
			Kind: groupBy, ID: identity.id, Name: identity.name, Summary: summary,
		})
	}
	sortUsageGroups(out)
	return out, nil
}

func usageGroupIdentity(event domain.UsageEvent, groupBy string) (string, string) {
	switch groupBy {
	case "service":
		return event.ServiceID, event.ServiceName
	case "automation":
		return event.AutomationID, event.AutomationName
	case "model":
		return event.ModelID, event.ModelName
	case "device":
		return event.DeviceID, event.DeviceName
	case "grant":
		return event.GrantScope + ":" + event.GrantScopeID, event.GrantScopeName
	}
	return "", ""
}

func buildUsageGroups(
	values map[string][]usageRollup,
	names map[string]string,
	groupBy string,
	from, to *time.Time,
) []domain.UsageGroup {
	out := make([]domain.UsageGroup, 0, len(values))
	for id, rollups := range values {
		out = append(out, domain.UsageGroup{
			Kind: groupBy, ID: id, Name: names[id],
			Summary: summarizeUsageRollups(rollups, from, to),
		})
	}
	sortUsageGroups(out)
	return out
}

func sortUsageGroups(values []domain.UsageGroup) {
	weight := func(summary domain.UsageSummary) int64 {
		var total int64
		for _, value := range []*int64{
			summary.Tokens.Input, summary.Tokens.Output,
			summary.Tokens.CacheRead, summary.Tokens.CacheWrite,
		} {
			if value != nil {
				total += *value
			}
		}
		return total
	}
	sort.Slice(values, func(i, j int) bool {
		left, right := weight(values[i].Summary), weight(values[j].Summary)
		if left != right {
			return left > right
		}
		return values[i].ID < values[j].ID
	})
}
