package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/google/uuid"
)

func categorySvc() (service.CategoryService, *memCategoryRepo, uuid.UUID) {
	repo := newMemCategoryRepo()
	return service.NewCategoryService(repo), repo, uuid.New()
}

func TestCategoryCreateValidation(t *testing.T) {
	svc, _, merchant := categorySvc()
	for _, tc := range []struct {
		name string
		req  model.CreateCategoryRequest
		want error
	}{
		{"empty name", model.CreateCategoryRequest{Name: ""}, service.ErrCategoryInvalidName},
		{"blank name", model.CreateCategoryRequest{Name: "   "}, service.ErrCategoryInvalidName},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.Create(context.Background(), merchant, tc.req); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}

	cat, err := svc.Create(context.Background(), merchant, model.CreateCategoryRequest{Name: "  Beans  "})
	if err != nil {
		t.Fatalf("valid create: %v", err)
	}
	if cat.Name != "Beans" {
		t.Fatalf("name not trimmed: %q", cat.Name)
	}
	if _, err := svc.Create(context.Background(), merchant, model.CreateCategoryRequest{Name: "beans"}); !errors.Is(err, service.ErrCategoryNameTaken) {
		t.Fatalf("duplicate name: got %v", err)
	}
}

func TestCategoryUpdateValidation(t *testing.T) {
	svc, _, merchant := categorySvc()
	seed, err := svc.Create(context.Background(), merchant, model.CreateCategoryRequest{Name: "Beans"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := svc.Update(context.Background(), merchant, seed.ID, model.UpdateCategoryRequest{Name: strptr("   ")}); !errors.Is(err, service.ErrCategoryInvalidName) {
		t.Fatalf("blank update: got %v", err)
	}
	if _, err := svc.Update(context.Background(), uuid.New(), seed.ID, model.UpdateCategoryRequest{Name: strptr("Hijack")}); !errors.Is(err, service.ErrCategoryNotFound) {
		t.Fatalf("foreign update: got %v", err)
	}
	got, err := svc.Get(context.Background(), merchant, seed.ID)
	if err != nil || got.Name != "Beans" {
		t.Fatalf("unchanged after invalid updates: %+v %v", got, err)
	}
}
