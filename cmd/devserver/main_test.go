package main

import (
	"context"
	"testing"

	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
)

func TestNewDevServerHandlerUsesDemoStoreWhenEnabled(t *testing.T) {
	t.Setenv(envDemoCatalogStore, "1")
	t.Setenv(catalog.EnvTableName, "")

	handler, err := newDevServerHandler(context.Background())
	if err != nil {
		t.Fatalf("newDevServerHandler returned error: %v", err)
	}
	store, ok := handler.Catalog().(*catalog.MemoryStore)
	if !ok {
		t.Fatalf("handler catalog store = %T, want *catalog.MemoryStore", handler.Catalog())
	}

	products, err := store.ListActiveProducts(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListActiveProducts returned error: %v", err)
	}
	if len(products) != 11 {
		t.Fatalf("active demo product count = %d, want 11", len(products))
	}
}

func TestNewDevServerHandlerKeepsEnvironmentStoreByDefault(t *testing.T) {
	t.Setenv(envDemoCatalogStore, "")
	t.Setenv(catalog.EnvTableName, "")

	handler, err := newDevServerHandler(context.Background())
	if err != nil {
		t.Fatalf("newDevServerHandler returned error: %v", err)
	}
	if _, ok := handler.Catalog().(catalog.EmptyStore); !ok {
		t.Fatalf("handler catalog store = %T, want catalog.EmptyStore", handler.Catalog())
	}
}

func TestNewDevServerHandlerRequiresExactDemoStoreFlag(t *testing.T) {
	t.Setenv(envDemoCatalogStore, "true")
	t.Setenv(catalog.EnvTableName, "")

	handler, err := newDevServerHandler(context.Background())
	if err != nil {
		t.Fatalf("newDevServerHandler returned error: %v", err)
	}
	if _, ok := handler.Catalog().(catalog.EmptyStore); !ok {
		t.Fatalf("handler catalog store = %T, want catalog.EmptyStore", handler.Catalog())
	}
}
