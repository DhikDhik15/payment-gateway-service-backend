package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
)

var (
	ErrSettlementNotFound            = errors.New("settlement not found")
	ErrSettlementAlreadyExists       = errors.New("settlement already exists")
	ErrSettlementImportInvalid       = errors.New("settlement import invalid")
	ErrSettlementImportConflict      = errors.New("settlement import conflict")
	ErrSettlementInvalidStatus       = errors.New("settlement invalid status")
	ErrReconciliationNotFound        = errors.New("reconciliation not found")
	ErrReconciliationRunning         = errors.New("reconciliation already running")
	ErrUnknownSettlementProvider     = errors.New("unknown settlement provider")
	ErrSettlementProviderUnsupported = errors.New("settlement provider is not configured")
)

// SettlementService handles import and read APIs for settlements.
type SettlementService interface {
	Import(ctx context.Context, req model.ImportSettlementRequest) (*model.Settlement, bool, error) // bool = replayed
	GetByID(ctx context.Context, id uuid.UUID) (*model.SettlementDetailResponse, error)
	List(ctx context.Context, filter model.SettlementListFilter) (*ListSettlementsResult, error)
}

type ListSettlementsResult struct {
	Settlements []model.Settlement
	Total       int64
	TotalPages  int
	Page        int
	Limit       int
}

type settlementService struct {
	repo               repository.SettlementRepository
	importers          map[string]SettlementImporter
	configuredProvider string
}

func NewSettlementService(repo repository.SettlementRepository, importers ...SettlementImporter) SettlementService {
	m := make(map[string]SettlementImporter, len(importers))
	for _, im := range importers {
		m[strings.ToUpper(im.ProviderName())] = im
	}
	return &settlementService{repo: repo, importers: m}
}

// NewSettlementServiceForProvider is the production-aware constructor. The
// current repository has only a mock settlement importer; non-mock providers
// must not accept an operator payload as if it were provider-verified data.
func NewSettlementServiceForProvider(repo repository.SettlementRepository, configuredProvider string, importers ...SettlementImporter) SettlementService {
	m := make(map[string]SettlementImporter, len(importers))
	for _, im := range importers {
		m[strings.ToUpper(im.ProviderName())] = im
	}
	return &settlementService{
		repo:               repo,
		importers:          m,
		configuredProvider: strings.ToLower(strings.TrimSpace(configuredProvider)),
	}
}

func (s *settlementService) Import(ctx context.Context, req model.ImportSettlementRequest) (*model.Settlement, bool, error) {
	if s.configuredProvider != "" && !strings.EqualFold(s.configuredProvider, mockProviderName) {
		return nil, false, ErrSettlementProviderUnsupported
	}
	provider := strings.ToUpper(strings.TrimSpace(req.Provider))
	ref := strings.TrimSpace(req.SettlementRef)
	if provider == "" || ref == "" {
		return nil, false, ErrSettlementImportInvalid
	}
	if len(req.Payload) == 0 || len(req.Payload) > maxSettlementPayloadBytes {
		return nil, false, ErrSettlementImportInvalid
	}

	importer, ok := s.importers[provider]
	if !ok {
		return nil, false, ErrUnknownSettlementProvider
	}

	parsed, err := importer.Parse(ctx, ref, req.Payload)
	if err != nil {
		if errors.Is(err, ErrSettlementImportInvalid) {
			return nil, false, err
		}
		return nil, false, fmt.Errorf("%w: %v", ErrSettlementImportInvalid, err)
	}
	parsed.Provider = provider
	parsed.SettlementRef = ref

	existing, err := s.repo.GetByProviderAndRef(ctx, provider, ref)
	if err == nil {
		if existing.PayloadHash == parsed.PayloadHash {
			slog.Info("settlement import idempotent replay",
				slog.String("provider", provider),
				slog.String("settlement_ref", ref),
				slog.String("settlement_id", existing.ID.String()),
			)
			return existing, true, nil
		}
		return nil, false, ErrSettlementImportConflict
	}
	if !errors.Is(err, repository.ErrSettlementNotFound) {
		return nil, false, err
	}

	now := time.Now().UTC()
	settlement := &model.Settlement{
		ID:             uuid.New(),
		Provider:       provider,
		SettlementRef:  ref,
		SettlementDate: parsed.SettlementDate,
		Currency:       parsed.Currency,
		GrossAmount:    parsed.GrossAmount,
		FeeAmount:      parsed.FeeAmount,
		NetAmount:      parsed.NetAmount,
		Status:         model.SettlementStatusImported,
		Source:         parsed.Source,
		PayloadHash:    parsed.PayloadHash,
		RawPayload:     redactSettlementPayload(parsed.RawPayload),
		ItemCount:      len(parsed.Items),
		ImportedAt:     now,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	items := make([]*model.SettlementItem, 0, len(parsed.Items))
	for _, it := range parsed.Items {
		items = append(items, &model.SettlementItem{
			ID:                    uuid.New(),
			SettlementID:          settlement.ID,
			Provider:              provider,
			ItemRef:               it.ItemRef,
			ItemType:              it.ItemType,
			ProviderTransactionID: it.ProviderTransactionID,
			ProviderRefundID:      it.ProviderRefundID,
			OrderID:               it.OrderID,
			Currency:              it.Currency,
			GrossAmount:           it.GrossAmount,
			FeeAmount:             it.FeeAmount,
			NetAmount:             it.NetAmount,
			SettledAt:             it.SettledAt,
			RawPayload:            it.RawPayload,
			CreatedAt:             now,
		})
	}

	if err := s.repo.CreateWithItems(ctx, settlement, items); err != nil {
		if errors.Is(err, repository.ErrSettlementAlreadyExists) {
			// Race: another import won — treat as replay/conflict based on hash.
			existing, getErr := s.repo.GetByProviderAndRef(ctx, provider, ref)
			if getErr != nil {
				return nil, false, ErrSettlementAlreadyExists
			}
			if existing.PayloadHash == parsed.PayloadHash {
				return existing, true, nil
			}
			return nil, false, ErrSettlementImportConflict
		}
		return nil, false, err
	}

	slog.Info("settlement imported",
		slog.String("settlement_id", settlement.ID.String()),
		slog.String("provider", provider),
		slog.String("settlement_ref", ref),
		slog.Int("item_count", settlement.ItemCount),
	)
	return settlement, false, nil
}

func (s *settlementService) GetByID(ctx context.Context, id uuid.UUID) (*model.SettlementDetailResponse, error) {
	st, err := s.repo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrSettlementNotFound) {
			return nil, ErrSettlementNotFound
		}
		return nil, err
	}
	items, err := s.repo.ListItems(ctx, id)
	if err != nil {
		return nil, err
	}
	return &model.SettlementDetailResponse{Settlement: *st, Items: items}, nil
}

func (s *settlementService) List(ctx context.Context, filter model.SettlementListFilter) (*ListSettlementsResult, error) {
	if filter.Page <= 0 {
		filter.Page = model.DefaultPage
	}
	if filter.Limit <= 0 {
		filter.Limit = model.DefaultLimit
	}
	if filter.Limit > model.MaxLimit {
		filter.Limit = model.MaxLimit
	}
	total, err := s.repo.Count(ctx, filter)
	if err != nil {
		return nil, err
	}
	rows, err := s.repo.List(ctx, filter)
	if err != nil {
		return nil, err
	}
	out := make([]model.Settlement, 0, len(rows))
	for _, r := range rows {
		out = append(out, *r)
	}
	pages := int(total) / filter.Limit
	if int(total)%filter.Limit != 0 {
		pages++
	}
	return &ListSettlementsResult{Settlements: out, Total: total, TotalPages: pages, Page: filter.Page, Limit: filter.Limit}, nil
}

// redactSettlementPayload strips known secret-like keys before persistence.
func redactSettlementPayload(raw []byte) []byte {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil // refuse to store unparseable raw blobs with potential secrets
	}
	redactMap(v)
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

func redactMap(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			lk := strings.ToLower(k)
			if strings.Contains(lk, "secret") || strings.Contains(lk, "password") ||
				strings.Contains(lk, "credential") || strings.Contains(lk, "api_key") ||
				strings.Contains(lk, "server_key") || strings.Contains(lk, "authorization") {
				t[k] = "[REDACTED]"
				continue
			}
			redactMap(child)
		}
	case []any:
		for _, el := range t {
			redactMap(el)
		}
	}
}
