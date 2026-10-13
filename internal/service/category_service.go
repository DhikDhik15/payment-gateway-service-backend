package service

import (
	"context"
	"errors"
	"strings"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
)

var (
	ErrCategoryNotFound    = errors.New("category not found")
	ErrCategoryNameTaken   = errors.New("category name already exists")
	ErrCategoryInvalidName = errors.New("category name is required")
)

type CategoryService interface {
	Create(ctx context.Context, merchantID uuid.UUID, req model.CreateCategoryRequest) (*model.Category, error)
	List(ctx context.Context, merchantID uuid.UUID) ([]model.Category, error)
	Get(ctx context.Context, merchantID uuid.UUID, categoryID uuid.UUID) (*model.Category, error)
	Update(ctx context.Context, merchantID uuid.UUID, categoryID uuid.UUID, req model.UpdateCategoryRequest) (*model.Category, error)
	Delete(ctx context.Context, merchantID uuid.UUID, categoryID uuid.UUID) error
}

type categoryService struct {
	repo repository.CategoryRepository
}

func NewCategoryService(repo repository.CategoryRepository) CategoryService {
	return &categoryService{repo: repo}
}

func (s *categoryService) Create(ctx context.Context, merchantID uuid.UUID, req model.CreateCategoryRequest) (*model.Category, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, ErrCategoryInvalidName
	}
	category := &model.Category{
		ID:          uuid.New(),
		MerchantID:  merchantID,
		Name:        name,
		Description: strings.TrimSpace(req.Description),
	}
	if err := s.repo.Create(ctx, category); err != nil {
		if errors.Is(err, repository.ErrCategoryNameExists) {
			return nil, ErrCategoryNameTaken
		}
		return nil, err
	}
	return category, nil
}

func (s *categoryService) List(ctx context.Context, merchantID uuid.UUID) ([]model.Category, error) {
	return s.repo.ListByMerchant(ctx, merchantID)
}

func (s *categoryService) Get(ctx context.Context, merchantID uuid.UUID, categoryID uuid.UUID) (*model.Category, error) {
	cat, err := s.repo.GetByID(ctx, merchantID, categoryID)
	if err != nil {
		if errors.Is(err, repository.ErrCategoryNotFound) {
			return nil, ErrCategoryNotFound
		}
		return nil, err
	}
	return cat, nil
}

func (s *categoryService) Update(ctx context.Context, merchantID uuid.UUID, categoryID uuid.UUID, req model.UpdateCategoryRequest) (*model.Category, error) {
	current, err := s.repo.GetByID(ctx, merchantID, categoryID)
	if err != nil {
		if errors.Is(err, repository.ErrCategoryNotFound) {
			return nil, ErrCategoryNotFound
		}
		return nil, err
	}
	if req.Name != nil {
		current.Name = strings.TrimSpace(*req.Name)
	}
	if req.Description != nil {
		current.Description = strings.TrimSpace(*req.Description)
	}
	if current.Name == "" {
		return nil, ErrCategoryInvalidName
	}
	if err := s.repo.Update(ctx, current); err != nil {
		if errors.Is(err, repository.ErrCategoryNameExists) {
			return nil, ErrCategoryNameTaken
		}
		if errors.Is(err, repository.ErrCategoryNotFound) {
			return nil, ErrCategoryNotFound
		}
		return nil, err
	}
	return current, nil
}

func (s *categoryService) Delete(ctx context.Context, merchantID uuid.UUID, categoryID uuid.UUID) error {
	if err := s.repo.Delete(ctx, merchantID, categoryID); err != nil {
		if errors.Is(err, repository.ErrCategoryNotFound) {
			return ErrCategoryNotFound
		}
		return err
	}
	return nil
}
